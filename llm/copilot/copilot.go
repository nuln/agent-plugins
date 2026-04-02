package copilot

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "copilot",
		PluginType:  "llm",
		Description: "GitHub Copilot CLI integration",
		Fields: []agent.ConfigField{
			{EnvVar: "COPILOT_GITHUB_TOKEN", Key: "github_token", Description: "GitHub token for authentication (overrides copilot login)", Type: agent.ConfigFieldSecret},
			{EnvVar: "COPILOT_HOME", Key: "copilot_home", Description: "Override Copilot config directory (~/.copilot)", Type: agent.ConfigFieldString},
			{Key: "work_dir", Description: "Working directory for Copilot", Default: ".", Type: agent.ConfigFieldString},
			{Key: "model", Description: "Model name", Type: agent.ConfigFieldString, Example: "claude-sonnet-4.6"},
			{Key: "mode", Description: "Permission mode", Type: agent.ConfigFieldEnum, AllowedValues: []string{"default", "autopilot", "yolo"}},
		},
	})

	agent.RegisterLLM("copilot", New)
}

// LLM drives GitHub Copilot CLI using `-p` prompt mode with `--output-format json`
// (JSONL) and `--stream on` for real-time streaming.
//
// Permission modes:
//   - "default":   ask permission for each tool call
//   - "autopilot": auto-approve continuation (--autopilot)
//   - "yolo":      auto-approve everything (--yolo)
//
// Environment variable overrides:
//   - COPILOT_GITHUB_TOKEN: Personal access token for GitHub authentication
//   - COPILOT_HOME: Custom config directory location (~/.copilot by default)
//   - COPILOT_PROVIDER_BASE_URL: Custom LLM provider endpoint (for enterprise)
type LLM struct {
	workDir     string
	model       string
	mode        string // "default" | "autopilot" | "yolo"
	githubToken string
	copilotHome string
	sessionEnv  []string
	providers   []agent.ProviderConfig
	activeIdx   int // -1 = no provider set (use env defaults)

	mu sync.Mutex
}

// New creates a new Copilot LLM instance.
func New(opts map[string]any) (agent.LLM, error) {
	workDir := readStringOpt(opts, "work_dir", "")
	if workDir == "" {
		workDir = "."
	}
	model := readStringOpt(opts, "model", "")
	mode := normalizeMode(readStringOpt(opts, "mode", "default"))
	githubToken := readStringOpt(opts, "github_token", os.Getenv("COPILOT_GITHUB_TOKEN"))
	copilotHome := readStringOpt(opts, "copilot_home", os.Getenv("COPILOT_HOME"))

	if _, err := exec.LookPath("copilot"); err != nil {
		return nil, fmt.Errorf("copilot: 'copilot' CLI not found in PATH, please install GitHub Copilot CLI first")
	}

	return &LLM{
		workDir:     workDir,
		model:       model,
		mode:        mode,
		githubToken: githubToken,
		copilotHome: copilotHome,
		activeIdx:   -1,
	}, nil
}

func normalizeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "autopilot", "auto", "auto-pilot":
		return "autopilot"
	case "yolo", "bypass", "allow-all":
		return "yolo"
	default:
		return "default"
	}
}

func (a *LLM) Name() string { return "copilot" }

func (a *LLM) Description() string {
	return "GitHub Copilot - AI-powered coding assistant with multi-model support"
}

// StartSession creates a new Copilot CLI session.
func (a *LLM) StartSession(ctx context.Context, sessionID string) (agent.AgentSession, error) {
	a.mu.Lock()
	model := a.model
	mode := a.mode
	workDir := a.workDir
	extraEnv := make([]string, len(a.sessionEnv))
	copy(extraEnv, a.sessionEnv)
	if a.githubToken != "" {
		extraEnv = append(extraEnv, "COPILOT_GITHUB_TOKEN="+a.githubToken)
	}
	if a.copilotHome != "" {
		extraEnv = append(extraEnv, "COPILOT_HOME="+a.copilotHome)
	}
	extraEnv = append(extraEnv, a.providerEnvLocked()...)
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	return newCopilotSession(ctx, workDir, model, mode, sessionID, extraEnv), nil
}

func (a *LLM) Stop() error { return nil }

func (a *LLM) Reload(opts map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	workDir := readStringOpt(opts, "work_dir", a.workDir)
	if workDir == "" {
		workDir = "."
	}
	model := readStringOpt(opts, "model", a.model)
	mode := normalizeMode(readStringOpt(opts, "mode", a.mode))
	githubToken := readStringOpt(opts, "github_token", os.Getenv("COPILOT_GITHUB_TOKEN"))
	copilotHome := readStringOpt(opts, "copilot_home", os.Getenv("COPILOT_HOME"))

	changed := false
	if workDir != a.workDir {
		a.workDir = workDir
		changed = true
	}
	if model != a.model {
		a.model = model
		changed = true
	}
	if mode != a.mode {
		a.mode = mode
		changed = true
	}
	if githubToken != a.githubToken {
		a.githubToken = githubToken
		changed = true
	}
	if copilotHome != a.copilotHome {
		a.copilotHome = copilotHome
		changed = true
	}

	if changed {
		slog.Info("copilot: reloaded config", "work_dir", a.workDir, "model", a.model, "mode", a.mode, "home_configured", a.copilotHome != "", "token_configured", a.githubToken != "")
	}
	return nil
}

// ── ModelSwitcher implementation ─────────────────────────────

func (a *LLM) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("copilot: model changed", "model", model)
}

func (a *LLM) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func (a *LLM) AvailableModels(_ context.Context) []agent.ModelOption {
	return []agent.ModelOption{
		{Name: "claude-sonnet-4.6", Desc: "Claude Sonnet 4.6 (default, balanced)"},
		{Name: "claude-opus-4.6", Desc: "Claude Opus 4.6 (most capable)"},
		{Name: "claude-opus-4.6-fast", Desc: "Claude Opus 4.6 Fast"},
		{Name: "claude-sonnet-4.5", Desc: "Claude Sonnet 4.5"},
		{Name: "claude-haiku-4.5", Desc: "Claude Haiku 4.5 (fastest)"},
		{Name: "gpt-5.2", Desc: "GPT-5.2"},
		{Name: "gpt-5.1-mini", Desc: "GPT-5.1 Mini"},
		{Name: "o4-mini", Desc: "O4 Mini (fast reasoning)"},
		{Name: "o3", Desc: "O3"},
		{Name: "gemini-2.5-pro", Desc: "Gemini 2.5 Pro"},
		{Name: "gemini-2.5-flash", Desc: "Gemini 2.5 Flash"},
	}
}

// ── ModeSwitcher implementation ──────────────────────────────

func (a *LLM) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = normalizeMode(mode)
	slog.Info("copilot: mode changed", "mode", a.mode)
}

func (a *LLM) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *LLM) PermissionModes() []agent.PermissionModeInfo {
	return []agent.PermissionModeInfo{
		{Key: "default", Name: "Default", NameZh: "默认", Desc: "Ask permission for each tool call", DescZh: "每次工具调用都需确认"},
		{Key: "autopilot", Name: "Autopilot", NameZh: "自动驾驶", Desc: "Auto-approve continuation without confirmation", DescZh: "自动继续无需确认"},
		{Key: "yolo", Name: "YOLO", NameZh: "YOLO 模式", Desc: "Auto-approve everything", DescZh: "全部自动通过"},
	}
}

// ── SessionEnvInjector implementation ────────────────────────

func (a *LLM) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
}

// ── WorkDirSwitcher implementation ───────────────────────────

func (a *LLM) SetWorkDir(dir string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if dir == "" {
		dir = "."
	}
	a.workDir = dir
	slog.Info("copilot: work dir changed", "dir", dir)
}

func (a *LLM) GetWorkDir() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workDir
}

// ── MemoryFileProvider implementation ────────────────────────

func (a *LLM) ProjectMemoryFile() string {
	absDir, err := filepath.Abs(a.workDir)
	if err != nil {
		absDir = a.workDir
	}
	return filepath.Join(absDir, ".copilot", "instructions.md")
}

func (a *LLM) GlobalMemoryFile() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(homeDir, ".copilot", "instructions.md")
}

// ── ContextCompressor implementation ─────────────────────────

func (a *LLM) CompressCommand() string { return "/compact" }

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
		slog.Info("copilot: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("copilot: provider switched", "provider", name)
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
// ProviderConfig field mapping:
//   - APIKey  → COPILOT_GITHUB_TOKEN (overrides copilot login credential)
//   - BaseURL → COPILOT_PROVIDER_BASE_URL (for private/custom LLM endpoints)
//   - Env     → arbitrary env vars (e.g. COPILOT_HOME for credential isolation)
func (a *LLM) providerEnvLocked() []string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	env := make([]string, 0, len(p.Env)+2)
	if p.APIKey != "" {
		env = append(env, "COPILOT_GITHUB_TOKEN="+p.APIKey)
	}
	if p.BaseURL != "" {
		env = append(env, "COPILOT_PROVIDER_BASE_URL="+p.BaseURL)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func readStringOpt(opts map[string]any, key, fallback string) string {
	if opts == nil {
		return fallback
	}
	v, _ := opts[key].(string)
	if v == "" {
		return fallback
	}
	return v
}
