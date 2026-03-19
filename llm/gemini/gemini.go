package gemini

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterLLM("gemini", New)
}

// LLM drives the Gemini CLI in headless mode using -p --output-format stream-json.
type LLM struct {
	cmd      string
	workDir  string
	model    string
	mode     string
	timeout  time.Duration
	extraEnv []string
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

	return &LLM{
		cmd:      cmd,
		workDir:  workDir,
		model:    model,
		mode:     mode,
		timeout:  timeout,
		extraEnv: extraEnv,
	}, nil
}

func (a *LLM) Name() string { return "gemini" }

func (a *LLM) Description() string {
	return "Google Gemini - High performance multimodal AI models"
}

func (a *LLM) StartSession(ctx context.Context, sessionID string) (agent.AgentSession, error) {
	return newGeminiSession(ctx, a.cmd, a.workDir, a.model, a.mode, sessionID, a.extraEnv, a.timeout), nil
}

func (a *LLM) Stop() error { return nil }

func (a *LLM) Reload(opts map[string]any) error {
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

func (a *LLM) GetModel() string { return a.model }

func (a *LLM) SetModel(model string) { a.model = model }

func (a *LLM) SetMode(mode string) { a.mode = mode }

func (a *LLM) GetMode() string { return a.mode }

func (a *LLM) PermissionModes() []agent.PermissionModeInfo {
	return []agent.PermissionModeInfo{
		{Key: "plan", Name: "Plan", NameZh: "计划模式", Desc: "Review all tool calls before execution", DescZh: "执行前预览计划"},
		{Key: "auto_edit", Name: "Auto Edit", NameZh: "自动编辑", Desc: "Automatically approve file edits", DescZh: "自动批准文件编辑"},
		{Key: "yolo", Name: "YOLO", NameZh: "极致模式", Desc: "Complete autonomy (USE WITH CAUTION)", DescZh: "完全自主（请谨慎使用）"},
	}
}
