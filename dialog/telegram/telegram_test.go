package telegram

import (
	"testing"
	"time"
)

// ── allowList ────────────────────────────────────────────────────────────────

func TestAllowList(t *testing.T) {
	cases := []struct {
		af, uid string
		ok      bool
	}{
		{"", "anyone", true},
		{"*", "anyone", true},
		{"alice,bob", "alice", true},
		{"alice,bob", "bob", true},
		{"alice, bob", "bob", true},
		{"Alice,Bob", "alice", true},
		{"alice,bob", "charlie", false},
		{"  alice  ", "alice", true},
	}
	for _, tc := range cases {
		if got := allowList(tc.af, tc.uid); got != tc.ok {
			t.Errorf("allowList(%q,%q)=%v want %v", tc.af, tc.uid, got, tc.ok)
		}
	}
}

// ── isOldMessage ─────────────────────────────────────────────────────────────

func TestIsOldMessage(t *testing.T) {
	if !isOldMessage(time.Now().Add(-6 * time.Minute)) {
		t.Error("6min-old message should be old")
	}
	if isOldMessage(time.Now().Add(-4 * time.Minute)) {
		t.Error("4min-old message should be fresh")
	}
	if isOldMessage(time.Now()) {
		t.Error("just-now message should be fresh")
	}
}

// ── isValidTelegramCommand ───────────────────────────────────────────────────

func TestIsValidTelegramCommand(t *testing.T) {
	cases := []struct {
		cmd   string
		valid bool
	}{
		{"start", true},
		{"a", true},
		{"hello_world", true},
		{"cmd123", true},
		{"", false},
		{"1start", false},
		{"cmd-name", false},
		{"cmd name", false},
		{"CMD", false},
		{string(make([]byte, 33)), false},
	}
	for _, tc := range cases {
		if got := isValidTelegramCommand(tc.cmd); got != tc.valid {
			t.Errorf("isValidTelegramCommand(%q)=%v want %v", tc.cmd, got, tc.valid)
		}
	}
}

// ── Reload ───────────────────────────────────────────────────────────────────

func TestReload_AllowFromUpdated(t *testing.T) {
	p := &TelegramAccess{token: "tok", allowFrom: "alice"}
	err := p.Reload(map[string]any{"allow_from": "alice,bob"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p.mu.RLock()
	af := p.allowFrom
	p.mu.RUnlock()
	if af != "alice,bob" {
		t.Errorf("allowFrom=%q want alice,bob", af)
	}
}

func TestReload_GroupReplyAll(t *testing.T) {
	p := &TelegramAccess{token: "tok", groupReplyAll: false}
	_ = p.Reload(map[string]any{"group_reply_all": true})
	if !p.groupReplyAll {
		t.Error("groupReplyAll not updated to true")
	}
}

func TestReload_ShareSessionInChannel(t *testing.T) {
	p := &TelegramAccess{token: "tok", shareSessionInChannel: false}
	_ = p.Reload(map[string]any{"share_session_in_channel": true})
	if !p.shareSessionInChannel {
		t.Error("shareSessionInChannel not updated to true")
	}
}

func TestReload_NilOpts(t *testing.T) {
	p := &TelegramAccess{token: "tok", allowFrom: "alice"}
	if err := p.Reload(nil); err != nil {
		t.Errorf("Reload(nil) should not error: %v", err)
	}
	if p.allowFrom != "alice" {
		t.Error("allowFrom should not change with nil opts")
	}
}

func TestReload_NoChange_IsNoop(t *testing.T) {
	p := &TelegramAccess{
		token:        "tok",
		allowFrom:    "alice",
		groupReplyAll: true,
	}
	if err := p.Reload(map[string]any{"allow_from": "alice", "group_reply_all": true}); err != nil {
		t.Errorf("no-change Reload should not error: %v", err)
	}
}

// ── New ───────────────────────────────────────────────────────────────────────

func TestNew_MissingToken(t *testing.T) {
	_, err := New(map[string]any{})
	if err == nil {
		t.Error("expected error when token is missing")
	}
}

func TestNew_InvalidProxy(t *testing.T) {
	_, err := New(map[string]any{
		"token": "tok",
		"proxy": "://invalid-url",
	})
	if err == nil {
		t.Error("expected error for invalid proxy URL")
	}
}

func TestNew_ValidOpts(t *testing.T) {
	p, err := New(map[string]any{
		"token":                    "fake-token",
		"allow_from":               "u1,u2",
		"group_reply_all":          true,
		"share_session_in_channel": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ta := p.(*TelegramAccess)
	if ta.allowFrom != "u1,u2" {
		t.Errorf("allowFrom=%q want u1,u2", ta.allowFrom)
	}
	if !ta.groupReplyAll {
		t.Error("groupReplyAll should be true")
	}
	if !ta.shareSessionInChannel {
		t.Error("shareSessionInChannel should be true")
	}
}
