package gemini

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "gemini",
		PluginType:  "llm",
		Description: "Google Gemini CLI integration",
		Fields: []agent.ConfigField{
			{EnvVar: "GEMINI_API_KEY", Key: "api_key", Description: "Gemini API key", Type: agent.ConfigFieldSecret},
			{EnvVar: "GEMINI_CLI_HOME", Key: "gemini_home", Description: "Custom home directory for Gemini CLI (gemini creates .gemini/ inside this path, overrides ~/.gemini)", Type: agent.ConfigFieldString},
			{EnvVar: "GEMINI_CMD", Key: "cmd", Description: "Path to gemini CLI binary", Type: agent.ConfigFieldString},
			{Key: "work_dir", Description: "Working directory for Gemini", Type: agent.ConfigFieldString},
			{Key: "model", Description: "Model name", Type: agent.ConfigFieldString},
			{Key: "mode", Description: "Execution mode", Default: "plan", Type: agent.ConfigFieldString},
		},
	})

	agent.RegisterLLM("gemini", New)
}

// LLM drives the Gemini CLI in headless mode using -p --output-format stream-json.
type LLM struct {
	cmd        string
	workDir    string
	model      string
	mode       string
	geminiHome string // GEMINI_CLI_HOME override (empty = use default)
	timeout    time.Duration
	extraEnv   []string
	providers  []agent.ProviderConfig
	activeIdx  int // -1 = no provider set (use env defaults)
	sessionEnv []string

	mu sync.Mutex
}

func New(opts map[string]any) (agent.LLM, error) {
	cmd, _ := opts["cmd"].(string)
	if cmd == "" {
		cmd = os.Getenv("GEMINI_CMD")
	}
	if cmd == "" {
		if path, err := exec.LookPath("gemini"); err == nil {
			cmd = path
		} else {
			return nil, fmt.Errorf("gemini: cmd not specified and 'gemini' not found in PATH")
		}
	}
	workDir, _ := opts["work_dir"].(string)
	if workDir == "" {
		if wd, err := os.Getwd(); err == nil {
			workDir = wd
		}
	}
	model, _ := opts["model"].(string)
	mode, _ := opts["mode"].(string)
	if mode == "" {
		mode = "plan"
	}
	timeoutStr, _ := opts["timeout"].(string)
	timeout := 10 * time.Minute
	if timeoutStr != "" {
		if t, err := time.ParseDuration(timeoutStr); err == nil {
			timeout = t
		}
	}

	var extraEnv []string
	if env, ok := opts["env"].(map[string]any); ok {
		for k, v := range env {
			extraEnv = append(extraEnv, fmt.Sprintf("%s=%v", k, v))
		}
	}

	geminiHome, _ := opts["gemini_home"].(string)
	if geminiHome == "" {
		geminiHome = os.Getenv("GEMINI_CLI_HOME")
	}

	return &LLM{
		cmd:        cmd,
		workDir:    workDir,
		model:      model,
		mode:       mode,
		geminiHome: geminiHome,
		timeout:    timeout,
		extraEnv:   extraEnv,
		activeIdx:  -1,
	}, nil
}

func (a *LLM) Name() string { return "gemini" }

func (a *LLM) Description() string {
	return "Google Gemini - High performance multimodal AI models"
}

func (a *LLM) StartSession(ctx context.Context, sessionID string) (agent.AgentSession, error) {
	a.mu.Lock()
	model := a.model
	mode := a.mode
	env := make([]string, len(a.extraEnv))
	copy(env, a.extraEnv)
	env = append(env, a.providerEnvLocked()...)
	env = append(env, a.sessionEnv...)
	if a.activeIdx >= 0 && a.activeIdx < len(a.providers) {
		if m := a.providers[a.activeIdx].Model; m != "" {
			model = m
		}
	}
	// Inject gemini home dir (plugin-level; provider Env can override if needed)
	if a.geminiHome != "" {
		env = append(env, "GEMINI_CLI_HOME="+a.geminiHome)
	}
	a.mu.Unlock()

	return newGeminiSession(ctx, a.cmd, a.workDir, model, mode, sessionID, env, a.timeout), nil
}

func (a *LLM) Stop() error { return nil }

func (a *LLM) Reload(opts map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	var cmd string
	if opts != nil {
		cmd, _ = opts["cmd"].(string)
	}
	if cmd == "" {
		cmd = os.Getenv("GEMINI_CMD")
	}
	if cmd == "" {
		if path, err := exec.LookPath("gemini"); err == nil {
			cmd = path
		}
	}

	if cmd != "" && cmd != a.cmd {
		// Validate
		if _, err := exec.LookPath(cmd); err != nil {
			return fmt.Errorf("gemini reload: invalid cmd: %w", err)
		}
		a.cmd = cmd
		slog.Info("gemini: reloaded configuration", "cmd", cmd)
	}
	return nil
}

// ── ModelSwitcher implementation ─────────────────────────────

func (a *LLM) GetModel() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func (a *LLM) SetModel(model string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.model = model
	slog.Info("gemini: model changed", "model", model)
}

// ── ModeSwitcher implementation ──────────────────────────────

func (a *LLM) SetMode(mode string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = mode
	slog.Info("gemini: mode changed", "mode", mode)
}

func (a *LLM) GetMode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

func (a *LLM) PermissionModes() []agent.PermissionModeInfo {
	return []agent.PermissionModeInfo{
		{Key: "plan", Name: "Plan", NameZh: "计划模式", Desc: "Review all tool calls before execution", DescZh: "执行前预览计划"},
		{Key: "auto_edit", Name: "Auto Edit", NameZh: "自动编辑", Desc: "Automatically approve file edits", DescZh: "自动批准文件编辑"},
		{Key: "yolo", Name: "YOLO", NameZh: "极致模式", Desc: "Complete autonomy (USE WITH CAUTION)", DescZh: "完全自主（请谨慎使用）"},
	}
}

// ── SessionEnvInjector implementation ────────────────────────

func (a *LLM) SetSessionEnv(env []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessionEnv = env
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
		slog.Info("gemini: provider cleared")
		return true
	}
	for i, p := range a.providers {
		if p.Name == name {
			a.activeIdx = i
			slog.Info("gemini: provider switched", "provider", name)
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
func (a *LLM) providerEnvLocked() []string {
	if a.activeIdx < 0 || a.activeIdx >= len(a.providers) {
		return nil
	}
	p := a.providers[a.activeIdx]
	env := make([]string, 0, len(p.Env)+2)
	if p.APIKey != "" {
		env = append(env, "GEMINI_API_KEY="+p.APIKey)
	}
	for k, v := range p.Env {
		env = append(env, k+"="+v)
	}
	return env
}
