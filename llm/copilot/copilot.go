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
type LLM struct {
	workDir    string
	model      string
	mode       string // "default" | "autopilot" | "yolo"
	sessionEnv []string
	providers  []agent.ProviderConfig
	activeIdx  int // -1 = no provider set (use env defaults)

	mu sync.Mutex
}

// New creates a new Copilot LLM instance.
func New(opts map[string]any) (agent.LLM, error) {
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		workDir = "."
	}
	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	mode = normalizeMode(mode)

	if _, err := exec.LookPath("copilot"); err != nil {
		return nil, fmt.Errorf("copilot: 'copilot' CLI not found in PATH, please install GitHub Copilot CLI first")
	}

	return &LLM{
		workDir:   workDir,
		model:     model,
		mode:      mode,
		activeIdx: -1,
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
	extraEnv := make([]string, len(a.sessionEnv))
	copy(extraEnv, a.sessionEnv)
	extraEnv = append(extraEnv, a.providerEnvLocked()...)
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	a.mu.Unlock()

	return newCopilotSession(ctx, a.workDir, model, mode, sessionID, extraEnv), nil
}

func (a *LLM) Stop() error { return nil }

func (a *LLM) Reload(_ map[string]any) error {
	slog.Info("copilot: reloaded config")
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
