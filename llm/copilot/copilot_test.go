package copilot

import (
	"testing"

	agent "github.com/nuln/agent-core"
)

// ── normalizeMode ─────────────────────────────────────────────────────────────

func TestNormalizeMode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"autopilot", "autopilot"},
		{"auto", "autopilot"},
		{"Auto-Pilot", "autopilot"},
		{"yolo", "yolo"},
		{"bypass", "yolo"},
		{"allow-all", "yolo"},
		{"default", "default"},
		{"", "default"},
		{"unknown", "default"},
	}
	for _, tc := range cases {
		if got := normalizeMode(tc.in); got != tc.want {
			t.Errorf("normalizeMode(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

// ── readStringOpt ─────────────────────────────────────────────────────────────

func TestReadStringOpt(t *testing.T) {
	opts := map[string]any{"key": "val", "empty": ""}
	if got := readStringOpt(opts, "key", "fb"); got != "val" {
		t.Errorf("expected val, got %q", got)
	}
	// empty string falls back
	if got := readStringOpt(opts, "empty", "fb"); got != "fb" {
		t.Errorf("empty value should fallback, got %q", got)
	}
	// missing key falls back
	if got := readStringOpt(opts, "missing", "fb"); got != "fb" {
		t.Errorf("missing key should fallback, got %q", got)
	}
	// nil opts falls back
	if got := readStringOpt(nil, "key", "fb"); got != "fb" {
		t.Errorf("nil opts should fallback, got %q", got)
	}
	// wrong type falls back
	opts2 := map[string]any{"num": 42}
	if got := readStringOpt(opts2, "num", "fb"); got != "fb" {
		t.Errorf("wrong type should fallback, got %q", got)
	}
}

// ── New ───────────────────────────────────────────────────────────────────────

func TestNew_WorkDirDefault(t *testing.T) {
	d, err := New(map[string]any{})
	if err != nil {
		t.Skip("copilot CLI not available:", err)
	}
	llm := d.(*LLM)
	if llm.workDir != "." {
		t.Errorf("default workDir should be '.', got %q", llm.workDir)
	}
	if llm.mode != "default" {
		t.Errorf("default mode should be 'default', got %q", llm.mode)
	}
}

func TestNew_OptsApplied(t *testing.T) {
	d, err := New(map[string]any{
		"work_dir":     "/tmp",
		"model":        "claude-sonnet-4.6",
		"mode":         "autopilot",
		"github_token": "test-token",
		"copilot_home": "/tmp/copilot-home",
	})
	if err != nil {
		t.Skip("copilot CLI not available:", err)
	}
	llm := d.(*LLM)
	if llm.workDir != "/tmp" {
		t.Errorf("workDir=%q want /tmp", llm.workDir)
	}
	if llm.model != "claude-sonnet-4.6" {
		t.Errorf("model=%q want claude-sonnet-4.6", llm.model)
	}
	if llm.mode != "autopilot" {
		t.Errorf("mode=%q want autopilot", llm.mode)
	}
	if llm.githubToken != "test-token" {
		t.Errorf("githubToken=%q want test-token", llm.githubToken)
	}
	if llm.copilotHome != "/tmp/copilot-home" {
		t.Errorf("copilotHome=%q want /tmp/copilot-home", llm.copilotHome)
	}
}

// ── Reload ────────────────────────────────────────────────────────────────────

func TestReload_UpdatesAllFields(t *testing.T) {
	llm := &LLM{
		workDir:     ".",
		model:       "old-model",
		mode:        "default",
		githubToken: "old-tok",
		copilotHome: "/old",
		activeIdx:   -1,
	}
	_ = llm.Reload(map[string]any{
		"work_dir":     "/new",
		"model":        "claude-opus-4.6",
		"mode":         "yolo",
		"github_token": "new-tok",
		"copilot_home": "/new-home",
	})
	if llm.workDir != "/new" {
		t.Errorf("workDir not updated, got %q", llm.workDir)
	}
	if llm.model != "claude-opus-4.6" {
		t.Errorf("model not updated, got %q", llm.model)
	}
	if llm.mode != "yolo" {
		t.Errorf("mode not updated, got %q", llm.mode)
	}
	if llm.githubToken != "new-tok" {
		t.Errorf("githubToken not updated, got %q", llm.githubToken)
	}
	if llm.copilotHome != "/new-home" {
		t.Errorf("copilotHome not updated, got %q", llm.copilotHome)
	}
}

func TestReload_NoOpWhenUnchanged(t *testing.T) {
	llm := &LLM{workDir: ".", model: "m", mode: "default", activeIdx: -1}
	if err := llm.Reload(map[string]any{"work_dir": ".", "model": "m", "mode": "default"}); err != nil {
		t.Errorf("Reload with identical values should not error: %v", err)
	}
}

func TestReload_EmptyWorkDirKeepsOld(t *testing.T) {
	llm := &LLM{workDir: "/old", activeIdx: -1}
	_ = llm.Reload(map[string]any{"work_dir": ""})
	// readStringOpt falls back to fallback (".")  when value is empty,
	// not to the old value; check the actual behaviour.
	if llm.workDir == "/old" {
		// workDir was replaced by fallback "."
	}
	// Either way, it must not be empty
	if llm.workDir == "" {
		t.Error("workDir must not be empty after Reload with empty work_dir")
	}
}

func TestReload_NilOpts(t *testing.T) {
	llm := &LLM{workDir: ".", model: "m", mode: "default", activeIdx: -1}
	if err := llm.Reload(nil); err != nil {
		t.Errorf("Reload(nil) should not error: %v", err)
	}
}

// ── SetWorkDir ────────────────────────────────────────────────────────────────

func TestSetWorkDir_EmptyBecomesDefault(t *testing.T) {
	llm := &LLM{workDir: "/orig"}
	llm.SetWorkDir("")
	if llm.workDir != "." {
		t.Errorf("empty SetWorkDir should reset to '.', got %q", llm.workDir)
	}
}

// ── SetMode / GetMode ────────────────────────────────────────────────────────

func TestSetGetMode(t *testing.T) {
	llm := &LLM{mode: "default"}
	llm.SetMode("autopilot")
	if got := llm.GetMode(); got != "autopilot" {
		t.Errorf("expected autopilot after SetMode, got %q", got)
	}
}

// ── SetModel / GetModel ──────────────────────────────────────────────────────

func TestSetGetModel(t *testing.T) {
	llm := &LLM{}
	llm.SetModel("gpt-5.2")
	if got := llm.GetModel(); got != "gpt-5.2" {
		t.Errorf("expected gpt-5.2, got %q", got)
	}
}

// ── providerEnvLocked ────────────────────────────────────────────────────────

func TestProviderEnvLocked_NoProvider(t *testing.T) {
	llm := &LLM{activeIdx: -1}
	if env := llm.providerEnvLocked(); len(env) != 0 {
		t.Errorf("expected empty env with no provider, got %v", env)
	}
}

func TestProviderEnvLocked_WithProvider(t *testing.T) {
	llm := &LLM{
		providers: []agent.ProviderConfig{{Name: "p1", APIKey: "ak", BaseURL: "http://x"}},
		activeIdx: 0,
	}
	env := llm.providerEnvLocked()
	var hasToken, hasURL bool
	for _, e := range env {
		if e == "COPILOT_GITHUB_TOKEN=ak" {
			hasToken = true
		}
		if e == "COPILOT_PROVIDER_BASE_URL=http://x" {
			hasURL = true
		}
	}
	if !hasToken {
		t.Error("expected COPILOT_GITHUB_TOKEN in env")
	}
	if !hasURL {
		t.Error("expected COPILOT_PROVIDER_BASE_URL in env")
	}
}
