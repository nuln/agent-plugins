package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "codex",
		PluginType:  "llm",
		Description: "OpenAI Codex CLI integration",
		Fields: []agent.ConfigField{
			{EnvVar: "OPENAI_API_KEY", Key: "api_key", Description: "OpenAI API key", Type: agent.ConfigFieldSecret},
			{EnvVar: "OPENAI_BASE_URL", Key: "base_url", Description: "OpenAI API base URL", Type: agent.ConfigFieldString},
			{EnvVar: "CODEX_HOME", Key: "codex_home", Description: "Codex home directory for configuration", Type: agent.ConfigFieldString},
			{Key: "work_dir", Description: "Working directory for Codex", Default: ".", Type: agent.ConfigFieldString},
			{Key: "model", Description: "Model name", Type: agent.ConfigFieldString},
			{Key: "reasoning_effort", Description: "Reasoning effort level", Type: agent.ConfigFieldEnum, AllowedValues: []string{"low", "medium", "high", "xhigh"}},
			{Key: "mode", Description: "Execution mode", Type: agent.ConfigFieldEnum, AllowedValues: []string{"suggest", "auto-edit", "full-auto", "yolo"}},
		},
	})

	agent.RegisterLLM("codex", New)
}

// LLM drives OpenAI Codex CLI using `codex exec --json`.
//
// Modes (maps to codex exec flags):
//   - "suggest":   default, no special flags (safe commands only)
//   - "auto-edit": --full-auto (sandbox-protected auto execution)
//   - "full-auto": --full-auto (sandbox-protected auto execution)
//   - "yolo":      --dangerously-bypass-approvals-and-sandbox
type LLM struct {
	workDir         string
	model           string
	reasoningEffort string
	mode            string // "suggest" | "auto-edit" | "full-auto" | "yolo"
	providers       []agent.ProviderConfig
	activeIdx       int // -1 = no provider set
	sessionEnv      []string
	mu              sync.Mutex
}

func New(opts map[string]any) (agent.LLM, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	reasoningEffort, _ := opts["reasoning_effort"].(string)
	mode, _ := opts["mode"].(string)
	mode = normalizeMode(mode)

	if _, err := exec.LookPath("codex"); err != nil {
		return nil, fmt.Errorf("codex: 'codex' CLI not found in PATH, install with: npm install -g @openai/codex")
	}

	return &LLM{
		workDir:         workDir,
		model:           model,
		reasoningEffort: normalizeReasoningEffort(reasoningEffort),
		mode:            mode,
		activeIdx:       -1,
	}, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "auto-edit", "autoedit", "auto_edit", "edit":
		return "auto-edit"
	case "full-auto", "fullauto", "full_auto", "auto":
		return "full-auto"
	case "yolo", "bypass", "dangerously-bypass":
		return "yolo"
	default:
		return "suggest"
	}
}

func normalizeReasoningEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case "low":
		return "low"
	case "medium", "med":
		return "medium"
	case "high":
		return "high"
	case "xhigh", "x-high", "very-high":
		return "xhigh"
	default:
		return ""
	}
}

func (a *LLM) Name() string { return "codex" }

func (a *LLM) Description() string {
	return "OpenAI Codex - Advanced code generation and reasoning and tool use"
}

func (a *LLM) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("codex: model changed", "model", model)
}

func (a *LLM) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func (a *LLM) SetReasoningEffort(effort string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reasoningEffort = normalizeReasoningEffort(effort)
	slog.Info("codex: reasoning effort changed", "reasoning_effort", a.reasoningEffort)
}

func (a *LLM) GetReasoningEffort() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reasoningEffort
}

func (a *LLM) AvailableReasoningEfforts() []string {
	return []string{"low", "medium", "high", "xhigh"}
}

func (a *LLM) AvailableModels(ctx context.Context) []agent.ModelOption {
	if models := a.fetchModelsFromAPI(ctx); len(models) > 0 {
		return models
	}
	if models := readCodexCachedModels(); len(models) > 0 {
		return models
	}
	return []agent.ModelOption{
		{Name: "o4-mini", Desc: "O4 Mini (fast reasoning)"},
		{Name: "o3", Desc: "O3 (most capable reasoning)"},
		{Name: "gpt-4.1", Desc: "GPT-4.1 (balanced)"},
		{Name: "gpt-4.1-mini", Desc: "GPT-4.1 Mini (fast)"},
		{Name: "gpt-4.1-nano", Desc: "GPT-4.1 Nano (fastest)"},
		{Name: "codex-mini-latest", Desc: "Codex Mini (code-optimized)"},
	}
}

var openaiChatModels = map[string]bool{
	"o4-mini": true, "o3": true, "o3-mini": true, "o1": true, "o1-mini": true,
	"gpt-4.1": true, "gpt-4.1-mini": true, "gpt-4.1-nano": true,
	"gpt-4o": true, "gpt-4o-mini": true,
	"codex-mini-latest": true,
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
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		return nil
	}
	if baseURL == "" {
		baseURL = os.Getenv("OPENAI_BASE_URL")
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/v1/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Debug("codex: failed to fetch models", "error", err)
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
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}

	models := make([]agent.ModelOption, 0, len(result.Data))
	for _, m := range result.Data {
		if openaiChatModels[m.ID] {
			models = append(models, agent.ModelOption{Name: m.ID})
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models
}

func readCodexCachedModels() []agent.ModelOption {
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		codexHome = filepath.Join(home, ".codex")
	}
	path := filepath.Join(codexHome, "models_cache.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var payload struct {
		Models []struct {
			Slug           string `json:"slug"`
			DisplayName    string `json:"display_name"`
			Description    string `json:"description"`
			Visibility     string `json:"visibility"`
			SupportedInAPI bool   `json:"supported_in_api"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return nil
	}

	models := make([]agent.ModelOption, 0, len(payload.Models))
	seen := make(map[string]struct{}, len(payload.Models))
	for _, m := range payload.Models {
		name := strings.TrimSpace(m.Slug)
		if name == "" {
			name = strings.TrimSpace(m.DisplayName)
		}
		if name == "" {
			continue
		}
		if m.Visibility != "" && m.Visibility != "list" {
			continue
		}
		if !m.SupportedInAPI {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		models = append(models, agent.ModelOption{
			Name: name,
			Desc: strings.TrimSpace(m.Description),
		})
	}
	return models
}

func (a *LLM) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

func (a *LLM) StartSession(ctx context.Context, sessionID string) (agent.AgentSession, error) {
	a.mu.Lock()
	mode := a.mode
	model := a.model
	reasoningEffort := a.reasoningEffort
	extraEnv := a.providerEnvLocked()
	extraEnv = append(extraEnv, a.sessionEnv...)
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	return newCodexSession(ctx, a.workDir, model, reasoningEffort, mode, sessionID, extraEnv), nil
}

func (a *LLM) ListSessions(_ context.Context) ([]agent.AgentSessionInfo, error) {
	return listCodexSessions(a.workDir)
}

func (a *LLM) GetSessionHistory(_ context.Context, sessionID string, limit int) ([]agent.HistoryEntry, error) {
	return getSessionHistory(sessionID, limit)
}

func (a *LLM) DeleteSession(_ context.Context, sessionID string) error {
	path := findSessionFile(sessionID)
	if path == "" {
		return fmt.Errorf("session file not found: %s", sessionID)
	}
	return os.Remove(path)
}

func (a *LLM) Stop() error { return nil }

func (a *LLM) Reload(opts map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if opts != nil {
		_, _ = opts["api_key"].(string)
	}
	_ = os.Getenv("OPENAI_API_KEY")

	// Codex doesn't store apiKey directly but uses it for API calls.
	// Reloading here could mean refreshing cached models or similar.
	slog.Info("codex: reloaded config")
	return nil
}

// SetMode changes the approval mode for future sessions.
func (a *LLM) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("codex: approval mode changed", "mode", a.mode)
}

func (a *LLM) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

// ── SkillProvider implementation ──────────────────────────────

func (a *LLM) SkillDirs() []string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	dirs := []string{
		filepath.Join(absDir, ".codex", "skills"),
		filepath.Join(absDir, ".claude", "skills"),
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			codexHome = filepath.Join(home, ".codex")
		}
	}
	if codexHome != "" {
		dirs = append(dirs, filepath.Join(codexHome, "skills"))
	}
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
	return filepath.Join(absDir, "AGENTS.md")
}

func (a *LLM) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(homeDir, ".codex")
	}
	return filepath.Join(codexHome, "AGENTS.md")
}

// ── ProviderSwitcher implementation ──────────────────────────

func (a *LLM) SetProviders(providers []agent.ProviderConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.providers = providers
}

func (a *LLM) SetActiveProvider(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if name == "" {
		a.activeIdx = -1
		slog.Info("codex: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("codex: provider switched", "provider", name)
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

func (a *LLM) providerEnvLocked() []string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	env := make([]string, 0, len(p.Env)+2) // +2 for OPENAI_API_KEY and OPENAI_BASE_URL
	if p.APIKey != "" {
		env = append(env, "OPENAI_API_KEY="+p.APIKey)
	}
	if p.BaseURL != "" {
		env = append(env, "OPENAI_BASE_URL="+p.BaseURL)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func (a *LLM) PermissionModes() []agent.PermissionModeInfo {
	return []agent.PermissionModeInfo{
		{Key: "suggest", Name: "Suggest", NameZh: "建议", Desc: "Ask permission for every tool call", DescZh: "每次工具调用都需确认"},
		{Key: "auto-edit", Name: "Auto Edit", NameZh: "自动编辑", Desc: "Auto-approve file edits, ask for shell commands", DescZh: "自动允许文件编辑，Shell 命令需确认"},
		{Key: "full-auto", Name: "Full Auto", NameZh: "全自动", Desc: "Auto-approve with workspace sandbox", DescZh: "自动通过（工作区沙箱）"},
		{Key: "yolo", Name: "YOLO", NameZh: "YOLO 模式", Desc: "Bypass all approvals and sandbox", DescZh: "跳过所有审批和沙箱"},
	}
}
