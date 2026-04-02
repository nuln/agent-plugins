package codex

import (
	"testing"

	agent "github.com/nuln/agent-core"
)

// ── normalizeMode ─────────────────────────────────────────────────────────────

func TestNormalizeMode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"auto-edit", "auto-edit"},
		{"autoedit", "auto-edit"},
		{"edit", "auto-edit"},
		{"full-auto", "full-auto"},
		{"auto", "full-auto"},
		{"fullauto", "full-auto"},
		{"yolo", "yolo"},
		{"bypass", "yolo"},
		{"dangerously-bypass", "yolo"},
		{"suggest", "suggest"},
		{"", "suggest"},
		{"unknown", "suggest"},
	}
	for _, tc := range cases {
		if got := normalizeMode(tc.in); got != tc.want {
			t.Errorf("normalizeMode(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

// ── normalizeReasoningEffort ──────────────────────────────────────────────────

func TestNormalizeReasoningEffort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"low", "low"},
		{"medium", "medium"},
		{"med", "medium"},
		{"high", "high"},
		{"xhigh", "xhigh"},
		{"x-high", "xhigh"},
		{"very-high", "xhigh"},
		{"", ""},
		{"unknown", ""},
	}
	for _, tc := range cases {
		if got := normalizeReasoningEffort(tc.in); got != tc.want {
			t.Errorf("normalizeReasoningEffort(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

// ── New ───────────────────────────────────────────────────────────────────────

func TestNew_WorkDirDefault(t *testing.T) {
	d, err := New(map[string]any{})
	if err != nil {
		t.Skip("codex CLI not available:", err)
	}
	llm := d.(*LLM)
	if llm.workDir != "." {
		t.Errorf("default workDir should be '.', got %q", llm.workDir)
	}
	if llm.mode != "suggest" {
		t.Errorf("default mode should be 'suggest', got %q", llm.mode)
	}
	if llm.activeIdx != -1 {
		t.Errorf("activeIdx should be -1, got %d", llm.activeIdx)
	}
}

func TestNew_OptsApplied(t *testing.T) {
	d, err := New(map[string]any{
		"work_dir":         "/tmp",
		"model":            "o3",
		"reasoning_effort": "high",
		"mode":             "full-auto",
		"api_key":          "sk-test",
		"base_url":         "https://api.example.com",
		"codex_home":       "/tmp/codex",
	})
	if err != nil {
		t.Skip("codex CLI not available:", err)
	}
	llm := d.(*LLM)
	if llm.workDir != "/tmp" {
		t.Errorf("workDir=%q want /tmp", llm.workDir)
	}
	if llm.model != "o3" {
		t.Errorf("model=%q want o3", llm.model)
	}
	if llm.reasoningEffort != "high" {
		t.Errorf("reasoningEffort=%q want high", llm.reasoningEffort)
	}
	if llm.mode != "full-auto" {
		t.Errorf("mode=%q want full-auto", llm.mode)
	}
	if llm.apiKey != "sk-test" {
		t.Errorf("apiKey=%q want sk-test", llm.apiKey)
	}
	if llm.baseURL != "https://api.example.com" {
		t.Errorf("baseURL=%q want https://api.example.com", llm.baseURL)
	}
	if llm.codexHome != "/tmp/codex" {
		t.Errorf("codexHome=%q want /tmp/codex", llm.codexHome)
	}
}

// ── Reload ────────────────────────────────────────────────────────────────────

func TestReload_UpdatesAllFields(t *testing.T) {
	llm := &LLM{
		workDir:         ".",
		model:           "o1",
		reasoningEffort: "low",
		mode:            "suggest",
		apiKey:          "old-key",
		baseURL:         "https://old.url",
		codexHome:       "/old",
		activeIdx:       -1,
	}
	_ = llm.Reload(map[string]any{
		"work_dir":         "/new",
		"model":            "o3",
		"reasoning_effort": "xhigh",
		"mode":             "yolo",
		"api_key":          "new-key",
		"base_url":         "https://new.url",
		"codex_home":       "/new-home",
	})
	if llm.workDir != "/new" {
		t.Errorf("workDir=%q want /new", llm.workDir)
	}
	if llm.model != "o3" {
		t.Errorf("model=%q want o3", llm.model)
	}
	if llm.reasoningEffort != "xhigh" {
		t.Errorf("effort=%q want xhigh", llm.reasoningEffort)
	}
	if llm.mode != "yolo" {
		t.Errorf("mode=%q want yolo", llm.mode)
	}
	if llm.apiKey != "new-key" {
		t.Errorf("apiKey=%q want new-key", llm.apiKey)
	}
	if llm.baseURL != "https://new.url" {
		t.Errorf("baseURL=%q want https://new.url", llm.baseURL)
	}
	if llm.codexHome != "/new-home" {
		t.Errorf("codexHome=%q want /new-home", llm.codexHome)
	}
}

func TestReload_NilOpts(t *testing.T) {
	llm := &LLM{workDir: ".", mode: "suggest", activeIdx: -1}
	if err := llm.Reload(nil); err != nil {
		t.Errorf("Reload(nil) should not error: %v", err)
	}
}

func TestReload_EmptyWorkDirFallsBackToDot(t *testing.T) {
	llm := &LLM{workDir: "/old", activeIdx: -1}
	_ = llm.Reload(map[string]any{"work_dir": ""})
	if llm.workDir == "" {
		t.Error("workDir must not be empty after Reload with empty work_dir")
	}
}

// ── SetMode / GetMode ────────────────────────────────────────────────────────

func TestSetGetMode(t *testing.T) {
	llm := &LLM{mode: "suggest"}
	llm.SetMode("yolo")
	if got := llm.GetMode(); got != "yolo" {
		t.Errorf("expected yolo, got %q", got)
	}
}

// ── providerEnvLocked ────────────────────────────────────────────────────────

func TestProviderEnvLocked_NoProvider(t *testing.T) {
	llm := &LLM{activeIdx: -1}
	if env := llm.providerEnvLocked(); len(env) != 0 {
		t.Errorf("expected empty env, got %v", env)
	}
}

func TestProviderEnvLocked_InjectsAPIKeyAndBaseURL(t *testing.T) {
	llm := &LLM{
		providers: []agent.ProviderConfig{{Name: "p1", APIKey: "sk-test", BaseURL: "https://base.url"}},
		activeIdx: 0,
	}
	env := llm.providerEnvLocked()
	var hasKey, hasURL bool
	for _, e := range env {
		if e == "OPENAI_API_KEY=sk-test" {
			hasKey = true
		}
		if e == "OPENAI_BASE_URL=https://base.url" {
			hasURL = true
		}
	}
	if !hasKey {
		t.Error("expected OPENAI_API_KEY in env")
	}
	if !hasURL {
		t.Error("expected OPENAI_BASE_URL in env")
	}
}

// ── plugin-level env fields ──────────────────────────────────────────────────

func TestPluginEnvFields_Stored(t *testing.T) {
	llm := &LLM{
		apiKey:    "sk-inject",
		baseURL:   "https://ep.url",
		codexHome: "/ch",
		activeIdx: -1,
	}
	if llm.apiKey != "sk-inject" {
		t.Error("apiKey not stored")
	}
	if llm.baseURL != "https://ep.url" {
		t.Error("baseURL not stored")
	}
	if llm.codexHome != "/ch" {
		t.Error("codexHome not stored")
	}
}
