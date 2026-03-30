package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "claudecode",
		PluginType:  "llm",
		Description: "Anthropic Claude Code CLI integration",
		Fields: []agent.ConfigField{
			{EnvVar: "ANTHROPIC_API_KEY", Key: "api_key", Description: "Anthropic API key", Type: agent.ConfigFieldSecret},
			{EnvVar: "ANTHROPIC_BASE_URL", Key: "base_url", Description: "Anthropic API base URL", Default: "https://api.anthropic.com", Type: agent.ConfigFieldString},
			{EnvVar: "CLAUDE_CONFIG_DIR", Key: "config_dir", Description: "Custom config directory for Claude (overrides ~/.claude)", Type: agent.ConfigFieldString},
			{Key: "work_dir", Description: "Working directory for Claude Code", Default: ".", Type: agent.ConfigFieldString},
			{Key: "model", Description: "Model name (e.g. sonnet, opus, haiku)", Type: agent.ConfigFieldString, Example: "sonnet"},
			{Key: "mode", Description: "Permission mode", Type: agent.ConfigFieldEnum, AllowedValues: []string{"default", "acceptEdits", "plan", "bypassPermissions"}},
			{Key: "router_url", Description: "Claude Code Router URL", Type: agent.ConfigFieldString},
			{Key: "router_api_key", Description: "Claude Code Router API key", Type: agent.ConfigFieldSecret},
		},
	})

	agent.RegisterLLM("claudecode", New)
}

// LLM drives Claude Code CLI using --input-format stream-json
// and --permission-prompt-tool stdio for bidirectional communication.
//
// Permission modes (maps to Claude's --permission-mode):
//   - "default":           every tool call requires user approval
//   - "acceptEdits":       auto-approve file edit tools, ask for others
//   - "plan":              plan only, no execution until approved
//   - "bypassPermissions": auto-approve everything (YOLO mode)
type LLM struct {
	workDir      string
	model        string
	mode         string // "default" | "acceptEdits" | "plan" | "bypassPermissions"
	configDir    string // CLAUDE_CONFIG_DIR override (empty = use default ~/.claude)
	allowedTools []string
	providers    []agent.ProviderConfig
	activeIdx    int // -1 = no provider set
	sessionEnv   []string
	routerURL    string // Claude Code Router URL (e.g., "http://127.0.0.1:3456")
	routerAPIKey string // Claude Code Router API key (optional)

	providerProxy *ProviderProxy // local proxy for third-party providers
	proxyLocalURL string         // local URL of the proxy

	mu sync.Mutex
}

func New(opts map[string]any) (agent.LLM, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	mode = normalizePermissionMode(mode)

	var allowedTools []string
	if tools, ok := opts["allowed_tools"].([]any); ok {
		for _, t := range tools {
			if s, ok := t.(string); ok {
				allowedTools = append(allowedTools, s)
			}
		}
	}

	// Claude Code Router support
	routerURL, _ := opts["router_url"].(string)
	routerAPIKey, _ := opts["router_api_key"].(string)

	configDir, _ := opts["config_dir"].(string)
	if configDir == "" {
		configDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}

	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("claudecode: 'claude' CLI not found in PATH, please install Claude Code first")
	}

	return &LLM{
		workDir:      workDir,
		model:        model,
		mode:         mode,
		configDir:    configDir,
		allowedTools: allowedTools,
		activeIdx:    -1,
		routerURL:    routerURL,
		routerAPIKey: routerAPIKey,
	}, nil
}

// normalizePermissionMode maps user-friendly aliases to Claude CLI values.
func normalizePermissionMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "acceptedits", "accept-edits", "accept_edits", "edit":
		return "acceptEdits"
	case "plan":
		return "plan"
	case "bypasspermissions", "bypass-permissions", "bypass_permissions",
		"yolo", "auto":
		return "bypassPermissions"
	default:
		return "default"
	}
}

func (a *LLM) Name() string { return "claudecode" }
func (a *LLM) Description() string {
	return "Anthropic Claude Code - High-performance llm for coding tasks"
}
func (a *LLM) CLIBinaryName() string  { return "claude" }
func (a *LLM) CLIDisplayName() string { return "Claude" }

func (a *LLM) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("claudecode: model changed", "model", model)
}

func (a *LLM) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func (a *LLM) AvailableModels(ctx context.Context) []agent.ModelOption {
	if models := a.fetchModelsFromAPI(ctx); len(models) > 0 {
		return models
	}
	return []agent.ModelOption{
		{Name: "sonnet", Desc: "Claude Sonnet 4 (balanced)"},
		{Name: "opus", Desc: "Claude Opus 4 (most capable)"},
		{Name: "haiku", Desc: "Claude Haiku 3.5 (fastest)"},
	}
}

func (a *LLM) fetchModelsFromAPI(ctx context.Context) []agent.ModelOption {
	a.mu.Lock()
	apiKey := ""
	baseURL := ""
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		apiKey = a.providers[a.activeIdx].APIKey
		baseURL = a.providers[a.activeIdx].BaseURL
	}
	a.mu.Unlock()

	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = os.Getenv("ANTHROPIC_BASE_URL")
	}
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/v1/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Debug("claudecode: failed to fetch models", "error", err)
		return nil
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var result struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}

	models := make([]agent.ModelOption, 0, len(result.Data))
	for _, m := range result.Data {
		models = append(models, agent.ModelOption{Name: m.ID, Desc: m.DisplayName})
	}
	return models
}

func (a *LLM) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

// StartSession creates a persistent interactive Claude Code session.
func (a *LLM) StartSession(ctx context.Context, sessionID string) (agent.AgentSession, error) {
	a.mu.Lock()
	tools := make([]string, len(a.allowedTools))
	copy(tools, a.allowedTools)
	model := a.model
	extraEnv := a.providerEnvLocked()
	extraEnv = append(extraEnv, a.sessionEnv...)

	// Inject config dir (plugin-level, overrideable by provider Env)
	if a.configDir != "" {
		extraEnv = append(extraEnv, "CLAUDE_CONFIG_DIR="+a.configDir)
	}

	// Add Claude Code Router environment variables if configured
	if a.routerURL != "" {
		extraEnv = append(extraEnv, "ANTHROPIC_BASE_URL="+a.routerURL)
		// When using router, we need to prevent proxy interference
		extraEnv = append(extraEnv, "NO_PROXY=127.0.0.1")
		// Disable telemetry and cost warnings for cleaner router integration
		extraEnv = append(extraEnv, "DISABLE_TELEMETRY=true")
		extraEnv = append(extraEnv, "DISABLE_COST_WARNINGS=true")
	}
	if a.routerAPIKey != "" {
		extraEnv = append(extraEnv, "ANTHROPIC_API_KEY="+a.routerAPIKey)
	}

	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	return newClaudeSession(ctx, a.workDir, model, sessionID, a.mode, tools, extraEnv)
}

func (a *LLM) ListSessions(ctx context.Context) ([]agent.AgentSessionInfo, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("claudecode: cannot determine home dir: %w", err)
	}

	absWorkDir, err := filepath.Abs(a.workDir)
	if err != nil {
		return nil, fmt.Errorf("claudecode: resolve work_dir: %w", err)
	}

	projectDir := findProjectDir(homeDir, absWorkDir)
	if projectDir == "" {
		return nil, nil
	}

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("claudecode: read project dir: %w", err)
	}

	sessions := make([]agent.AgentSessionInfo, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		sessionID := strings.TrimSuffix(name, ".jsonl")
		info, err := entry.Info()
		if err != nil {
			continue
		}

		summary, msgCount := scanSessionMeta(filepath.Join(projectDir, name))

		sessions = append(sessions, agent.AgentSessionInfo{
			ID:           sessionID,
			Summary:      summary,
			MessageCount: msgCount,
			ModifiedAt:   info.ModTime(),
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModifiedAt.After(sessions[j].ModifiedAt)
	})

	return sessions, nil
}

func (a *LLM) DeleteSession(_ context.Context, sessionID string) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("claudecode: cannot determine home dir: %w", err)
	}
	absWorkDir, err := filepath.Abs(a.workDir)
	if err != nil {
		return fmt.Errorf("claudecode: resolve work_dir: %w", err)
	}
	projectDir := findProjectDir(homeDir, absWorkDir)
	if projectDir == "" {
		return fmt.Errorf("session not found")
	}
	path := filepath.Join(projectDir, sessionID+".jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("session file not found: %s", sessionID)
	}
	return os.Remove(path)
}

func scanSessionMeta(path string) (string, int) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer func() {
		_ = f.Close()
	}()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	var summary string
	var count int

	for scanner.Scan() {
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		if entry.Type == "user" || entry.Type == "assistant" {
			count++
			if entry.Type == "user" && entry.Message.Content != "" {
				summary = entry.Message.Content
			}
		}
	}
	summary = stripXMLTags(summary)
	summary = strings.TrimSpace(summary)
	if utf8.RuneCountInString(summary) > 40 {
		summary = string([]rune(summary)[:40]) + "..."
	}
	return summary, count
}

var xmlTagRe = regexp.MustCompile(`<[^>]+>`)

func stripXMLTags(s string) string {
	return xmlTagRe.ReplaceAllString(s, "")
}

// GetSessionHistory reads the Claude Code JSONL transcript and returns user/assistant messages.
func (a *LLM) GetSessionHistory(_ context.Context, sessionID string, limit int) ([]agent.HistoryEntry, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	absWorkDir, _ := filepath.Abs(a.workDir)
	projectDir := findProjectDir(homeDir, absWorkDir)
	if projectDir == "" {
		return nil, fmt.Errorf("claudecode: project dir not found")
	}

	path := filepath.Join(projectDir, sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("claudecode: open session file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	var entries []agent.HistoryEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)

	for scanner.Scan() {
		var raw struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(scanner.Bytes(), &raw) != nil {
			continue
		}
		if raw.Type != "user" && raw.Type != "assistant" {
			continue
		}

		ts, _ := time.Parse(time.RFC3339Nano, raw.Timestamp)
		text := extractTextContent(raw.Message.Content)
		if text == "" {
			continue
		}

		entries = append(entries, agent.HistoryEntry{
			Role:      raw.Type,
			Content:   text,
			Timestamp: ts,
		})
	}

	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries, nil
}

// extractTextContent extracts readable text from Claude Code message content.
// Content can be a plain string or an array of content blocks.
func extractTextContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try plain string first
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	// Try array of content blocks
	var blocks []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Thinking string `json:"thinking"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}

	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			return b.Text
		}
	}
	return ""
}

func (a *LLM) Stop() error { return nil }

func (a *LLM) Reload(opts map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	var apiKey string
	if opts != nil {
		apiKey, _ = opts["router_api_key"].(string)
		if apiKey == "" {
			apiKey, _ = opts["api_key"].(string)
		}
	}
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}

	if apiKey != "" && apiKey != a.routerAPIKey {
		a.routerAPIKey = apiKey
		slog.Info("claudecode: reloaded api key")
	}
	return nil
}

// SetMode changes the permission mode for future sessions.
func (a *LLM) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizePermissionMode(mode)
	slog.Info("claudecode: permission mode changed", "mode", a.mode)
}

// GetMode returns the current permission mode.
func (a *LLM) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

// PermissionModes returns all supported permission modes.
func (a *LLM) PermissionModes() []agent.PermissionModeInfo {
	return []agent.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Ask permission for every tool call", DescZh: "每次工具调用都需确认"},
		{Key: "acceptEdits", Name: "Accept Edits", NameZh: "接受编辑", Desc: "Auto-approve file edits, ask for others", DescZh: "自动允许文件编辑，其他需确认"},
		{Key: "plan", Name: "Plan Mode", NameZh: "计划模式", Desc: "Plan only, no execution until approved", DescZh: "只做规划不执行，审批后再执行"},
		{Key: "bypassPermissions", Name: "YOLO", NameZh: "YOLO 模式", Desc: "Auto-approve everything", DescZh: "全部自动通过"},
	}
}

// AddAllowedTools adds tools to the pre-allowed list (takes effect on next session).
func (a *LLM) AddAllowedTools(tools ...string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	existing := make(map[string]bool)
	for _, t := range a.allowedTools {
		existing[t] = true
	}
	for _, tool := range tools {
		if !existing[tool] {
			a.allowedTools = append(a.allowedTools, tool)
			existing[tool] = true
		}
	}
	slog.Info("claudecode: updated allowed tools", "tools", tools, "total", len(a.allowedTools))
	return nil
}

// GetAllowedTools returns the current list of pre-allowed tools.
func (a *LLM) GetAllowedTools() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]string, len(a.allowedTools))
	copy(result, a.allowedTools)
	return result
}

// ── CommandProvider implementation ────────────────────────────

func (a *LLM) CommandDirs() []string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	dirs := []string{filepath.Join(absDir, ".claude", "commands")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".claude", "commands"))
	}
	return dirs
}

// ── SkillProvider implementation ──────────────────────────────

func (a *LLM) SkillDirs() []string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	dirs := []string{filepath.Join(absDir, ".claude", "skills")}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".claude", "skills"))
	}
	return dirs
}

// ── ContextCompressor implementation ──────────────────────────

func (a *LLM) CompressCommand() string { return "/compact" }

// ── MemoryFileProvider implementation ─────────────────────────

func (a *LLM) ProjectMemoryFile() string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	return filepath.Join(absDir, "CLAUDE.md")
}

func (a *LLM) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".claude", "CLAUDE.md")
}

func (a *LLM) HasSystemPromptSupport() bool { return true }

// ── ProviderSwitcher implementation ──────────────────────────

func (a *LLM) SetProviders(providers []agent.ProviderConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.providers = providers
}

func (a *LLM) SetActiveProvider(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopProviderProxyLocked()
	if name == "" {
		a.activeIdx = -1
		slog.Info("claudecode: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("claudecode: provider switched", "provider", name)
			return true
		}
	}
	return false
}

func (a *LLM) GetActiveProvider() *agent.ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	return &p
}

func (a *LLM) ListProviders() []agent.ProviderConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]agent.ProviderConfig, len(a.providers))
	copy(result, a.providers)
	return result
}

// providerEnvLocked returns env vars for the active provider. Caller must hold mu.
//
// When a custom base_url is configured:
//  1. We use ANTHROPIC_AUTH_TOKEN (Bearer) instead of ANTHROPIC_API_KEY
//     (x-api-key). Claude Code validates API keys against api.anthropic.com
//     which hangs for third-party endpoints; Bearer auth skips that check.
//  2. If the provider sets thinking (e.g. "disabled"), a local reverse proxy
//     rewrites the thinking parameter for compatibility with providers that
//     don't support adaptive thinking.
func (a *LLM) providerEnvLocked() []string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		a.stopProviderProxyLocked()
		return nil
	}
	p := a.providers[a.activeIdx]
	env := make([]string, 0, len(p.Env)+4) // +4 for ANTHROPIC_BASE_URL, NO_PROXY, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_API_KEY

	if p.BaseURL != "" {
		if p.Thinking != "" {
			if err := a.ensureProviderProxyLocked(p.BaseURL, p.Thinking); err != nil {
				slog.Error("providerproxy: failed to start", "error", err)
				env = append(env, "ANTHROPIC_BASE_URL="+p.BaseURL)
			} else {
				env = append(env, "ANTHROPIC_BASE_URL="+a.proxyLocalURL)
				env = append(env, "NO_PROXY=127.0.0.1")
			}
		} else {
			a.stopProviderProxyLocked()
			env = append(env, "ANTHROPIC_BASE_URL="+p.BaseURL)
		}
		if p.APIKey != "" {
			env = append(env, "ANTHROPIC_AUTH_TOKEN="+p.APIKey)
			env = append(env, "ANTHROPIC_API_KEY=")
		}
	} else {
		a.stopProviderProxyLocked()
		if p.APIKey != "" {
			env = append(env, "ANTHROPIC_API_KEY="+p.APIKey)
		}
	}

	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func (a *LLM) ensureProviderProxyLocked(targetURL, thinkingOverride string) error {
	if a.providerProxy != nil && a.proxyLocalURL != "" {
		return nil
	}
	a.stopProviderProxyLocked()
	proxy, localURL, err := NewProviderProxy(targetURL, thinkingOverride)
	if err != nil {
		return err
	}
	a.providerProxy = proxy
	a.proxyLocalURL = localURL
	return nil
}

func (a *LLM) stopProviderProxyLocked() {
	if a.providerProxy != nil {
		a.providerProxy.Close()
		a.providerProxy = nil
		a.proxyLocalURL = ""
	}
}

// summarizeInput produces a short human-readable description of tool input.
func summarizeInput(tool string, input any) string {
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}

	switch tool {
	case "Read", "Edit", "Write":
		if fp, ok := m["file_path"].(string); ok {
			return fp
		}
	case "Bash":
		if cmd, ok := m["command"].(string); ok {
			return cmd
		}
	case "Grep":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
	case "Glob":
		if p, ok := m["pattern"].(string); ok {
			return p
		}
		if p, ok := m["glob_pattern"].(string); ok {
			return p
		}
	}

	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseUserQuestions extracts structured questions from AskUserQuestion input.
func parseUserQuestions(input map[string]any) []agent.UserQuestion {
	questionsRaw, ok := input["questions"].([]any)
	if !ok || len(questionsRaw) == 0 {
		return nil
	}
	var questions []agent.UserQuestion
	for _, qRaw := range questionsRaw {
		qMap, ok := qRaw.(map[string]any)
		if !ok {
			continue
		}
		q := agent.UserQuestion{
			Question:    strVal(qMap, "question"),
			Header:      strVal(qMap, "header"),
			MultiSelect: boolVal(qMap, "multiSelect"),
		}
		if optsRaw, ok := qMap["options"].([]any); ok {
			for _, oRaw := range optsRaw {
				oMap, ok := oRaw.(map[string]any)
				if !ok {
					continue
				}
				q.Options = append(q.Options, agent.UserQuestionOption{
					Label:       strVal(oMap, "label"),
					Description: strVal(oMap, "description"),
				})
			}
		}
		if q.Question != "" {
			questions = append(questions, q)
		}
	}
	return questions
}

func strVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func boolVal(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

// findProjectDir locates the Claude Code session directory for a given work dir.
// Claude Code stores sessions at ~/.claude/projects/{projectKey}/ where projectKey
// is derived from the absolute path. On Windows, the key format may vary (colon
// handling, slash direction), so we try multiple key candidates and fall back to
// scanning the projects directory.
func findProjectDir(homeDir, absWorkDir string) string {
	projectsBase := filepath.Join(homeDir, ".claude", "projects")

	// Build candidate keys: different ways Claude Code might encode the path.
	// Claude Code replaces path separators, colons, and underscores with "-".
	candidates := []string{
		// Unix-style: replace OS separator with "-"
		strings.ReplaceAll(absWorkDir, string(filepath.Separator), "-"),
		// Windows: replace both "\" and ":" with "-"
		strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(absWorkDir),
		// Claude Code also replaces underscores with "-"
		strings.NewReplacer("/", "-", "\\", "-", ":", "-", "_", "-").Replace(absWorkDir),
	}
	// Also try with forward slashes (config might use forward slashes on Windows)
	fwd := strings.ReplaceAll(absWorkDir, "\\", "/")
	candidates = append(candidates, strings.ReplaceAll(fwd, "/", "-"))

	for _, key := range candidates {
		dir := filepath.Join(projectsBase, key)
		if _, err := os.Stat(dir); err == nil {
			return dir
		}
	}

	// Fallback: scan the projects directory and find a match by
	// comparing the tail of the encoded path (case-insensitive for Windows).
	entries, err := os.ReadDir(projectsBase)
	if err != nil {
		return ""
	}

	normWork := strings.ToLower(strings.NewReplacer("/", "-", "\\", "-", ":", "-", "_", "-").Replace(absWorkDir))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		normEntry := strings.ToLower(entry.Name())
		if normEntry == normWork {
			return filepath.Join(projectsBase, entry.Name())
		}
	}

	return ""
}
