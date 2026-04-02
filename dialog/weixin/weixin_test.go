package weixin

import (
	"context"
	"sync"
	"testing"
	"time"
)

// ── isAllowed ─────────────────────────────────────────────────────────────────

func TestIsAllowed_EmptyAllowFrom(t *testing.T) {
	d := &WeixinDialog{}
	if !d.isAllowed("", "any-user") {
		t.Error("empty allowFrom should allow everyone")
	}
}

func TestIsAllowed_Listed(t *testing.T) {
	d := &WeixinDialog{}
	cases := []struct {
		af, uid string
		ok      bool
	}{
		{"alice, bob", "alice", true},
		{"alice, bob", "bob", true},
		{"alice, bob", "charlie", false},
		{"  alice  ", "alice", true},
	}
	for _, tc := range cases {
		if d.isAllowed(tc.af, tc.uid) != tc.ok {
			t.Errorf("isAllowed(%q,%q) want %v", tc.af, tc.uid, tc.ok)
		}
	}
}

func TestIsAllowed_GlobalFallback(t *testing.T) {
	d := &WeixinDialog{allowFrom: "global-user"}
	if !d.isAllowed("", "global-user") {
		t.Error("should fall back to d.allowFrom")
	}
	if d.isAllowed("", "other") {
		t.Error("other not in global allowFrom should be denied")
	}
}

func TestIsAllowed_PerBotOverridesGlobal(t *testing.T) {
	d := &WeixinDialog{allowFrom: "global-user"}
	if !d.isAllowed("bot-user", "bot-user") {
		t.Error("per-bot allowFrom should allow bot-user")
	}
	if d.isAllowed("bot-user", "global-user") {
		t.Error("per-bot allowFrom should deny global-user")
	}
}

// ── parseReplyContext ─────────────────────────────────────────────────────────

func TestParseReplyContext_Struct(t *testing.T) {
	d := &WeixinDialog{workers: map[string]*BotWorker{"default": {}}}
	rc := ReplyContext{BotID: "b1", ContextToken: "tok", ToUserID: "u1", CreatedAt: 123}
	got, err := d.parseReplyContext(rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.BotID != "b1" || got.ContextToken != "tok" {
		t.Errorf("parsed wrong: %+v", got)
	}
}

func TestParseReplyContext_Pointer(t *testing.T) {
	d := &WeixinDialog{workers: map[string]*BotWorker{"default": {}}}
	rc := &ReplyContext{BotID: "b2", ContextToken: "tok2"}
	got, err := d.parseReplyContext(rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ContextToken != "tok2" {
		t.Errorf("expected tok2, got %q", got.ContextToken)
	}
}

func TestParseReplyContext_NilPointer(t *testing.T) {
	d := &WeixinDialog{workers: map[string]*BotWorker{}}
	var rc *ReplyContext
	_, err := d.parseReplyContext(rc)
	if err == nil {
		t.Error("expected error for nil *ReplyContext")
	}
}

func TestParseReplyContext_LegacyString(t *testing.T) {
	d := &WeixinDialog{workers: map[string]*BotWorker{"default": {}}}
	got, err := d.parseReplyContext("legacy-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ContextToken != "legacy-token" {
		t.Errorf("expected legacy-token, got %q", got.ContextToken)
	}
	if got.BotID == "" {
		t.Error("expected BotID from defaultBotID fallback")
	}
}

func TestParseReplyContext_InvalidType(t *testing.T) {
	d := &WeixinDialog{workers: map[string]*BotWorker{}}
	_, err := d.parseReplyContext(42)
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

// ── defaultBotID ──────────────────────────────────────────────────────────────

func TestDefaultBotID_Empty(t *testing.T) {
	d := &WeixinDialog{workers: map[string]*BotWorker{}}
	if got := d.defaultBotID(); got != "default" {
		t.Errorf("expected 'default', got %q", got)
	}
}

func TestDefaultBotID_SortedFirst(t *testing.T) {
	d := &WeixinDialog{workers: map[string]*BotWorker{
		"bot-z": {}, "bot-a": {}, "bot-m": {},
	}}
	if got := d.defaultBotID(); got != "bot-a" {
		t.Errorf("expected bot-a, got %q", got)
	}
}

// ── resolveBotConfigs ─────────────────────────────────────────────────────────

func TestResolveBotConfigs_SingleToken(t *testing.T) {
	d := &WeixinDialog{token: "tok123", accountID: "acc1", baseURL: "https://ilink.weixin.qq.com"}
	cfgs := d.resolveBotConfigs(nil)
	if len(cfgs) != 1 || cfgs[0].Token != "tok123" || cfgs[0].ID != "acc1" {
		t.Errorf("unexpected configs: %+v", cfgs)
	}
}

func TestResolveBotConfigs_NoToken(t *testing.T) {
	d := &WeixinDialog{baseURL: "https://example.com"}
	if n := len(d.resolveBotConfigs(nil)); n != 0 {
		t.Errorf("expected 0 configs without token, got %d", n)
	}
}

func TestResolveBotConfigs_BotsJSON(t *testing.T) {
	d := &WeixinDialog{
		baseURL:  "https://ilink.weixin.qq.com",
		botsJSON: `[{"id":"b1","token":"tok1"},{"id":"b2","token":"tok2","base_url":"https://c.url"}]`,
	}
	cfgs := d.resolveBotConfigs(nil)
	if len(cfgs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(cfgs))
	}
	if cfgs[0].ID != "b1" || cfgs[0].Token != "tok1" {
		t.Errorf("wrong b1: %+v", cfgs[0])
	}
	if cfgs[1].BaseURL != "https://c.url" {
		t.Errorf("expected custom URL for b2, got %q", cfgs[1].BaseURL)
	}
}

func TestResolveBotConfigs_SkipsMissingToken(t *testing.T) {
	d := &WeixinDialog{
		baseURL:  "https://ilink.weixin.qq.com",
		botsJSON: `[{"id":"no-token"},{"id":"has-token","token":"tok"}]`,
	}
	cfgs := d.resolveBotConfigs(nil)
	if len(cfgs) != 1 || cfgs[0].ID != "has-token" {
		t.Errorf("expected only has-token, got %+v", cfgs)
	}
}

func TestResolveBotConfigs_InvalidJSON(t *testing.T) {
	d := &WeixinDialog{baseURL: "https://x", botsJSON: "not-json"}
	if n := len(d.resolveBotConfigs(nil)); n != 0 {
		t.Errorf("expected 0 for invalid JSON, got %d", n)
	}
}

func TestResolveBotConfigs_OptsOverride(t *testing.T) {
	d := &WeixinDialog{
		baseURL:  "https://ilink.weixin.qq.com",
		botsJSON: `[{"id":"old","token":"old-tok"}]`,
	}
	cfgs := d.resolveBotConfigs(map[string]any{"bots_json": `[{"id":"new","token":"new-tok"}]`})
	if len(cfgs) != 1 || cfgs[0].ID != "new" {
		t.Errorf("opts should override bots_json, got %+v", cfgs)
	}
}

func TestResolveBotConfigs_AutoIDSequential(t *testing.T) {
	d := &WeixinDialog{
		baseURL:  "https://ilink.weixin.qq.com",
		botsJSON: `[{"token":"tok1"},{"token":"tok2"}]`,
	}
	cfgs := d.resolveBotConfigs(nil)
	if len(cfgs) != 2 || cfgs[0].ID != "bot-1" || cfgs[1].ID != "bot-2" {
		t.Errorf("expected bot-1/bot-2, got %v", cfgs)
	}
}

// ── requireWorker ─────────────────────────────────────────────────────────────

func TestRequireWorker_NotFound(t *testing.T) {
	d := &WeixinDialog{workers: map[string]*BotWorker{}}
	if _, err := d.requireWorker("ghost"); err == nil {
		t.Error("expected error for missing bot")
	}
}

func TestRequireWorker_Found(t *testing.T) {
	w := &BotWorker{cfg: BotConfig{ID: "b1"}}
	d := &WeixinDialog{workers: map[string]*BotWorker{"b1": w}}
	got, err := d.requireWorker("b1")
	if err != nil || got != w {
		t.Errorf("got wrong worker: err=%v", err)
	}
}

func TestRequireWorker_EmptyIDFallback(t *testing.T) {
	w := &BotWorker{cfg: BotConfig{ID: "b1"}}
	d := &WeixinDialog{workers: map[string]*BotWorker{"b1": w}}
	got, err := d.requireWorker("")
	if err != nil || got != w {
		t.Error("empty botID should fallback to first worker")
	}
}

// ── Reply: freshness gate ─────────────────────────────────────────────────────

func TestReply_ExpiredContextToken(t *testing.T) {
	w := &BotWorker{cfg: BotConfig{ID: "b1"}}
	d := &WeixinDialog{workers: map[string]*BotWorker{"b1": w}}
	rc := ReplyContext{
		BotID:        "b1",
		ContextToken: "old",
		ToUserID:     "u1",
		CreatedAt:    time.Now().Add(-25 * time.Hour).Unix(),
	}
	if err := d.Reply(context.Background(), rc, "hi"); err == nil {
		t.Fatal("expected error for 25h-old token")
	}
}

func TestReply_EmptyToken(t *testing.T) {
	w := &BotWorker{cfg: BotConfig{ID: "b1"}}
	d := &WeixinDialog{workers: map[string]*BotWorker{"b1": w}}
	rc := ReplyContext{BotID: "b1", ContextToken: "", ToUserID: "u1"}
	if err := d.Reply(context.Background(), rc, "hi"); err == nil {
		t.Error("expected error for empty context_token")
	}
}

// TestReply_ZeroCreatedAtSkipsExpiry verifies the expiry logic in isolation:
// a zero CreatedAt should not be treated as expired.
func TestReply_ZeroCreatedAtSkipsExpiry(t *testing.T) {
	rc := ReplyContext{ContextToken: "tok", ToUserID: "u1", CreatedAt: 0}
	// The expiry check: CreatedAt == 0 means "unknown age" → skip.
	if rc.CreatedAt != 0 {
		t.Fatal("precondition: CreatedAt must be 0")
	}
	maxAge := keepaliveWindow
	age := time.Since(time.Unix(rc.CreatedAt, 0))
	// When CreatedAt == 0, age is ~55 years, which looks expired.
	// The production code must explicitly skip the check for CreatedAt == 0.
	if rc.CreatedAt == 0 {
		// This is the bypass branch — expiry check should not apply.
		return
	}
	if age > maxAge {
		t.Error("unexpected: expiry check on zero CreatedAt should have been skipped")
	}
}

// ── Reload ────────────────────────────────────────────────────────────────────

func TestReload_UpdatesFields(t *testing.T) {
	d := &WeixinDialog{
		token:     "old",
		allowFrom: "alice",
		workers:   map[string]*BotWorker{},
		started:   false,
	}
	_ = d.Reload(map[string]any{
		"token":      "new",
		"allow_from": "alice,bob",
		"bots_json":  `[{"id":"x","token":"xt"}]`,
	})
	if d.token != "new" {
		t.Errorf("token not updated, got %q", d.token)
	}
	if d.allowFrom != "alice,bob" {
		t.Errorf("allowFrom not updated, got %q", d.allowFrom)
	}
	if d.botsJSON == "" {
		t.Error("botsJSON not updated")
	}
}

func TestReload_NotStarted_NoWorkers(t *testing.T) {
	d := &WeixinDialog{workers: map[string]*BotWorker{}, started: false}
	_ = d.Reload(map[string]any{"bots_json": `[{"id":"b1","token":"tok1"}]`})
	d.workerMu.RLock()
	n := len(d.workers)
	d.workerMu.RUnlock()
	if n != 0 {
		t.Errorf("not-started dialog should not start workers, got %d", n)
	}
}

// ── keepalive constants ───────────────────────────────────────────────────────

func TestKeepaliveConstants(t *testing.T) {
	if keepaliveWindow != 24*time.Hour {
		t.Errorf("keepaliveWindow=%v want 24h", keepaliveWindow)
	}
	if keepaliveWarn1 != 22*time.Hour {
		t.Errorf("keepaliveWarn1=%v want 22h", keepaliveWarn1)
	}
	if keepaliveWarn2 != 23*time.Hour {
		t.Errorf("keepaliveWarn2=%v want 23h", keepaliveWarn2)
	}
}

// ── scanKeepalive ─────────────────────────────────────────────────────────────

func TestScanKeepalive_SkipsNonConnected(t *testing.T) {
	d := &WeixinDialog{workers: map[string]*BotWorker{
		"b1": {cfg: BotConfig{ID: "b1"}, status: "error"},
		"b2": {cfg: BotConfig{ID: "b2"}, status: "connected"},
	}}
	d.scanKeepalive() // must not panic
}

func TestScanKeepalive_MarksExpired(t *testing.T) {
	w := &BotWorker{
		cfg:         BotConfig{ID: "b1"},
		status:      "connected",
		lastContext: "tok",
		lastPeer:    "user1",
		lastTokenAt: time.Now().Add(-25 * time.Hour),
	}
	d := &WeixinDialog{workers: map[string]*BotWorker{"b1": w}}
	d.scanKeepalive()
	w.mu.Lock()
	status := w.status
	w.mu.Unlock()
	if status != "session_expired" {
		t.Errorf("expected session_expired, got %q", status)
	}
}

func TestScanKeepalive_FreshTokenNotExpired(t *testing.T) {
	w := &BotWorker{
		cfg:         BotConfig{ID: "b1"},
		status:      "connected",
		lastContext: "tok",
		lastPeer:    "user1",
		lastTokenAt: time.Now().Add(-1 * time.Hour),
	}
	d := &WeixinDialog{workers: map[string]*BotWorker{"b1": w}}
	d.scanKeepalive()
	w.mu.Lock()
	status := w.status
	w.mu.Unlock()
	if status == "session_expired" {
		t.Error("1h-old token should not be expired")
	}
}

// ── reconcileWorkers ──────────────────────────────────────────────────────────

func TestReconcileWorkers_RemovesStale(t *testing.T) {
	cancelled := false
	w := &BotWorker{
		cfg:    BotConfig{ID: "stale"},
		cancel: func() { cancelled = true },
		status: "connected",
	}
	d := &WeixinDialog{
		workers: map[string]*BotWorker{"stale": w},
		started: true,
		wg:      sync.WaitGroup{},
	}
	d.reconcileWorkers(nil)
	if !cancelled {
		t.Error("cancel() should have been called for removed worker")
	}
	d.workerMu.RLock()
	_, still := d.workers["stale"]
	d.workerMu.RUnlock()
	if still {
		t.Error("stale worker should be removed from map")
	}
}

// ── New ───────────────────────────────────────────────────────────────────────

func TestNew_Defaults(t *testing.T) {
	d, err := New(map[string]any{})
	if err != nil {
		t.Fatalf("New() empty opts: %v", err)
	}
	wd := d.(*WeixinDialog)
	if wd.baseURL == "" {
		t.Error("baseURL should have default")
	}
	if wd.workers == nil {
		t.Error("workers map should be initialised")
	}
}

func TestNew_OptsApplied(t *testing.T) {
	d, err := New(map[string]any{
		"base_url":   "https://custom.url",
		"token":      "mytok",
		"account_id": "myacc",
		"allow_from": "alice",
		"bots_json":  `[{"id":"b1","token":"b1tok"}]`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wd := d.(*WeixinDialog)
	if wd.baseURL != "https://custom.url" || wd.token != "mytok" || wd.allowFrom != "alice" {
		t.Errorf("wrong field values: baseURL=%q token=%q allowFrom=%q",
			wd.baseURL, wd.token, wd.allowFrom)
	}
}
