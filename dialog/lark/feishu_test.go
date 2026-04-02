package lark

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
		{"*", "X", true},
		{"alice,bob", "alice", true},
		{"alice,bob", "bob", true},
		{"alice, bob", "BOB", true},
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
}

// ── New / required fields ─────────────────────────────────────────────────────

func TestNew_MissingAppID(t *testing.T) {
	_, err := New(map[string]any{"app_secret": "secret"})
	if err == nil {
		t.Error("expected error when app_id is missing")
	}
}

func TestNew_MissingAppSecret(t *testing.T) {
	_, err := New(map[string]any{"app_id": "id"})
	if err == nil {
		t.Error("expected error when app_secret is missing")
	}
}

func TestNew_BothMissing(t *testing.T) {
	_, err := New(map[string]any{})
	if err == nil {
		t.Error("expected error when both app_id and app_secret are missing")
	}
}

func TestNew_Defaults(t *testing.T) {
	d, err := New(map[string]any{"app_id": "id123", "app_secret": "sec123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	la := larkAccess(d)
	if la.reactionEmoji != "OnIt" {
		t.Errorf("default reactionEmoji=%q want OnIt", la.reactionEmoji)
	}
	if la.appID != "id123" {
		t.Errorf("appID=%q want id123", la.appID)
	}
}

func TestNew_ReactionEmojiNone(t *testing.T) {
	d, err := New(map[string]any{
		"app_id": "id", "app_secret": "sec",
		"reaction_emoji": "none",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	la := larkAccess(d)
	if la.reactionEmoji != "" {
		t.Errorf("reaction_emoji=none should result in empty emoji, got %q", la.reactionEmoji)
	}
}

func TestNew_DisableCard(t *testing.T) {
	d, err := New(map[string]any{
		"app_id": "id", "app_secret": "sec", "enable_feishu_card": false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	la := larkAccess(d)
	if la.useInteractiveCard {
		t.Error("useInteractiveCard should be false when enable_feishu_card=false")
	}
}

func TestNew_GroupReplyAll(t *testing.T) {
	d, err := New(map[string]any{
		"app_id": "id", "app_secret": "sec",
		"group_reply_all": true, "reply_in_thread": true, "share_session_in_channel": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	la := larkAccess(d)
	if !la.groupReplyAll {
		t.Error("groupReplyAll not set")
	}
	if !la.replyInThread {
		t.Error("replyInThread not set")
	}
	if !la.shareSessionInChannel {
		t.Error("shareSessionInChannel not set")
	}
}

// ── Reload ────────────────────────────────────────────────────────────────────

func TestReload_AllowFromUpdated(t *testing.T) {
	p := &LarkAccess{
		appID: "id", appSecret: "sec", allowFrom: "alice",
		domain: "https://open.feishu.cn",
	}
	_ = p.Reload(map[string]any{"app_id": "id", "app_secret": "sec", "allow_from": "alice,bob"})
	if p.allowFrom != "alice,bob" {
		t.Errorf("allowFrom=%q want alice,bob", p.allowFrom)
	}
}

func TestReload_ReactionEmoji(t *testing.T) {
	p := &LarkAccess{
		appID: "id", appSecret: "sec", reactionEmoji: "OnIt",
		domain: "https://open.feishu.cn",
	}
	_ = p.Reload(map[string]any{"app_id": "id", "app_secret": "sec", "reaction_emoji": "Eyes"})
	if p.reactionEmoji != "Eyes" {
		t.Errorf("reactionEmoji=%q want Eyes", p.reactionEmoji)
	}
}

func TestReload_ReactionEmojiNone(t *testing.T) {
	p := &LarkAccess{
		appID: "id", appSecret: "sec", reactionEmoji: "OnIt",
		domain: "https://open.feishu.cn",
	}
	_ = p.Reload(map[string]any{"app_id": "id", "app_secret": "sec", "reaction_emoji": "none"})
	if p.reactionEmoji != "" {
		t.Errorf("reaction_emoji none should clear emoji, got %q", p.reactionEmoji)
	}
}

func TestReload_GroupFlags(t *testing.T) {
	p := &LarkAccess{
		appID: "id", appSecret: "sec",
		groupReplyAll: false, shareSessionInChannel: false, replyInThread: false,
		domain: "https://open.feishu.cn",
	}
	_ = p.Reload(map[string]any{
		"app_id": "id", "app_secret": "sec",
		"group_reply_all": true, "share_session_in_channel": true, "reply_in_thread": true,
	})
	if !p.groupReplyAll {
		t.Error("groupReplyAll not updated")
	}
	if !p.shareSessionInChannel {
		t.Error("shareSessionInChannel not updated")
	}
	if !p.replyInThread {
		t.Error("replyInThread not updated")
	}
}

func TestReload_NilOpts(t *testing.T) {
	p := &LarkAccess{
		appID: "id", appSecret: "sec", allowFrom: "alice",
		domain: "https://open.feishu.cn",
	}
	if err := p.Reload(nil); err != nil {
		t.Errorf("Reload(nil) should not error: %v", err)
	}
	if p.allowFrom != "alice" {
		t.Error("allowFrom should not change on nil opts")
	}
}

func TestReload_NoChange_IsNoop(t *testing.T) {
	p := &LarkAccess{
		appID: "id", appSecret: "sec", allowFrom: "alice",
		reactionEmoji: "OnIt", domain: "https://open.feishu.cn",
	}
	if err := p.Reload(map[string]any{
		"app_id": "id", "app_secret": "sec",
		"allow_from": "alice", "reaction_emoji": "OnIt",
	}); err != nil {
		t.Errorf("no-change Reload should not error: %v", err)
	}
}

// ── helper ───────────────────────────────────────────────────────────────────

func larkAccess(d interface{}) *LarkAccess {
	if la, ok := d.(*LarkAccess); ok {
		return la
	}
	if ila, ok := d.(*InteractiveLarkAccess); ok {
		return ila.LarkAccess
	}
	panic("unexpected type")
}
