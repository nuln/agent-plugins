package weixin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "weixin",
		PluginType:  "dialog",
		Description: "Tencent Weixin messaging channel",
		Fields: []agent.ConfigField{
			{EnvVar: "WEIXIN_BASE_URL", Key: "base_url", Description: "Weixin backend API base URL", Required: true, Type: agent.ConfigFieldString, Default: "https://ilink.weixin.qq.com"},
			{EnvVar: "WEIXIN_TOKEN", Key: "token", Description: "Weixin bot token (auto-filled after login)", Type: agent.ConfigFieldSecret},
			{EnvVar: "WEIXIN_ACCOUNT_ID", Key: "account_id", Description: "Weixin bot account ID (auto-filled after login)", Type: agent.ConfigFieldString},
			{EnvVar: "WEIXIN_BOTS_JSON", Key: "bots_json", Description: "Multi-bot JSON config. Example: [{\"id\":\"bot-1\",\"token\":\"xxx\",\"base_url\":\"https://ilinkai.weixin.qq.com\"}]", Type: agent.ConfigFieldSecret},
			{Key: "allow_from", Description: "Comma-separated list of allowed user IDs", Type: agent.ConfigFieldString},
		},
	})

	agent.RegisterDialog("weixin", func(opts map[string]any) (agent.Dialog, error) {
		return New(opts)
	})
}

const (
	keepaliveWindow     = 24 * time.Hour
	keepaliveWarn1      = 22 * time.Hour
	keepaliveWarn2      = 23 * time.Hour
	keepaliveScanTicker = 10 * time.Minute
)

type ReplyContext struct {
	BotID        string `json:"bot_id"`
	ContextToken string `json:"context_token"`
	ToUserID     string `json:"to_user_id"`
	CreatedAt    int64  `json:"created_at"`
}

type BotConfig struct {
	ID        string
	BaseURL   string
	Token     string
	AccountID string
	AllowFrom string
}

type botJSONConfig struct {
	ID        string `json:"id"`
	BaseURL   string `json:"base_url"`
	Token     string `json:"token"`
	AccountID string `json:"account_id"`
	AllowFrom string `json:"allow_from"`
}

type BotWorker struct {
	cfg     BotConfig
	api     *ApiOptions
	cancel  context.CancelFunc
	buf     string
	status  string
	lastErr error

	mu            sync.RWMutex
	lastContext   string
	lastPeer      string
	lastTokenAt   time.Time
	warned22At    time.Time
	warned23At    time.Time
	lastInboundAt time.Time
}

type WeixinDialog struct {
	name           string
	baseURL        string
	token          string
	accountID      string
	allowFrom      string
	botsJSON       string
	handler        agent.MessageHandler
	cancel         context.CancelFunc
	workers        map[string]*BotWorker
	workerMu       sync.RWMutex
	reminderCancel context.CancelFunc
	started        bool
	wg             sync.WaitGroup
}

func New(opts map[string]any) (agent.Dialog, error) {
	baseURL, _ := opts["base_url"].(string)
	if baseURL == "" {
		baseURL = os.Getenv("WEIXIN_BASE_URL")
	}
	if baseURL == "" {
		baseURL = "https://ilink.weixin.qq.com"
	}
	token, _ := opts["token"].(string)
	if token == "" {
		token = os.Getenv("WEIXIN_TOKEN")
	}
	accountID, _ := opts["account_id"].(string)
	if accountID == "" {
		accountID = os.Getenv("WEIXIN_ACCOUNT_ID")
	}
	allowFrom, _ := opts["allow_from"].(string)
	botsJSON, _ := opts["bots_json"].(string)
	if botsJSON == "" {
		botsJSON = os.Getenv("WEIXIN_BOTS_JSON")
	}

	return &WeixinDialog{
		name:      "weixin",
		baseURL:   baseURL,
		token:     token,
		accountID: accountID,
		allowFrom: allowFrom,
		botsJSON:  botsJSON,
		workers:   map[string]*BotWorker{},
	}, nil
}

func (d *WeixinDialog) Name() string { return d.name }

func (d *WeixinDialog) Start(handler agent.MessageHandler) error {
	d.handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel
	d.started = true

	bots := d.resolveBotConfigs(nil)
	if len(bots) == 0 {
		// 默认启动第一个用户的扫码登录
		slog.Info("weixin: no bot configured, starting first user login...")
		go func() {
			if err := d.AddBotInteractive(ctx); err != nil {
				slog.Error("weixin: startup login failed", "error", err)
			}
		}()
	} else {
		for _, cfg := range bots {
			d.startWorker(ctx, cfg)
		}
	}

	remCtx, remCancel := context.WithCancel(ctx)
	d.reminderCancel = remCancel
	d.wg.Add(1)
	go d.keepaliveLoop(remCtx)

	return nil
}

// AddBotInteractive 是导出的接口，允许外部触发新的扫码登录流程
func (d *WeixinDialog) AddBotInteractive(ctx context.Context) error {
	api := &ApiOptions{BaseURL: FixedBaseURL, IlinkAppId: "bot"}
	
	qrResp, err := api.FetchQRCode(ctx, DefaultIlinkBotType)
	if err != nil {
		return fmt.Errorf("fetch qrcode failed: %w", err)
	}

	// 核心修复：根据 openclaw-weixin 源码，qrResp.QRCodeImgContent 
	// 才是真正包含 AppID 和授权逻辑的完整扫码 URL。
	loginURL := qrResp.QRCodeImgContent

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("  微信机器人扫码登录 (OpenClaw/iLink)")
	fmt.Println("  请使用手机微信扫描下方二维码进行登录：\n")

	// 直接在终端打印二维码
	config := qrterminal.Config{
		Level:     qrterminal.L,
		Writer:    os.Stdout,
		BlackChar: qrterminal.WHITE,
		WhiteChar: qrterminal.BLACK,
		QuietZone: 1,
	}
	qrterminal.GenerateWithConfig(loginURL, config)

	fmt.Printf("\n  如果无法扫描，请尝试在浏览器打开：\n  %s\n", loginURL)
	fmt.Println("\n" + strings.Repeat("=", 60) + "\n")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			status, err := api.PollQRStatus(ctx, qrResp.QRCode)
			if err != nil {
				continue
			}

			switch status.Status {
			case "wait":
				// 继续轮询
			case "scaned":
				slog.Info("weixin: qrcode scanned, waiting for confirmation...")
			case "confirmed":
				slog.Info("weixin: login confirmed!", "bot_id", status.IlinkBotID)
				return d.onLoginSuccess(ctx, status)
			case "expired":
				return fmt.Errorf("qrcode expired")
			}
		}
	}
}

func (d *WeixinDialog) onLoginSuccess(ctx context.Context, status *StatusResponse) error {
	cfg := BotConfig{
		ID:        status.IlinkBotID,
		Token:     status.BotToken,
		BaseURL:   status.BaseURL,
		AccountID: status.IlinkBotID,
	}

	// Persist to .env if possible (best effort)
	d.appendBotToEnv(cfg)

	d.startWorker(ctx, cfg)
	return nil
}

func (d *WeixinDialog) appendBotToEnv(cfg BotConfig) {
	// In a real implementation, we would update .env or a local config file.
	// For now, we log it clearly so the user can save it.
	slog.Info("weixin: login successful. Please save these credentials to your .env file:",
		"WEIXIN_TOKEN", cfg.Token,
		"WEIXIN_ACCOUNT_ID", cfg.AccountID,
		"WEIXIN_BASE_URL", cfg.BaseURL)
}

func (d *WeixinDialog) Stop() error {
	if d.reminderCancel != nil {
		d.reminderCancel()
	}

	if d.cancel != nil {
		d.cancel()
	}

	d.workerMu.Lock()
	for _, w := range d.workers {
		if w.cancel != nil {
			w.cancel()
		}
	}
	d.workers = map[string]*BotWorker{}
	d.workerMu.Unlock()

	d.wg.Wait()
	d.started = false
	return nil
}

func (d *WeixinDialog) Reply(ctx context.Context, replyCtx any, content string) error {
	rc, err := d.parseReplyContext(replyCtx)
	if err != nil {
		return err
	}
	if rc.ContextToken == "" {
		return fmt.Errorf("weixin: empty context_token")
	}

	if rc.CreatedAt > 0 {
		created := time.Unix(rc.CreatedAt, 0)
		if time.Since(created) > keepaliveWindow {
			return fmt.Errorf("weixin: context token expired (>24h), wait for user message to renew")
		}
	}

	w, err := d.requireWorker(rc.BotID)
	if err != nil {
		return err
	}

	req := &SendMessageReq{
		Msg: &WeixinMessage{
			ToUserID:     rc.ToUserID,
			ContextToken: rc.ContextToken,
			ItemList: []*MessageItem{
				{
					Type:     MessageItemTypeText,
					TextItem: &TextItem{Text: content},
				},
			},
		},
	}

	if err := w.api.SendMessage(ctx, req); err != nil {
		return err
	}

	w.mu.Lock()
	w.lastContext = rc.ContextToken
	w.lastPeer = rc.ToUserID
	w.mu.Unlock()

	return nil
}

func (d *WeixinDialog) Send(ctx context.Context, replyCtx any, content string) error {
	return d.Reply(ctx, replyCtx, content)
}

func (d *WeixinDialog) Reload(opts map[string]any) error {
	if token, _ := opts["token"].(string); token != "" {
		d.token = token
	}
	if allowFrom, _ := opts["allow_from"].(string); allowFrom != "" {
		d.allowFrom = allowFrom
	}
	if botsJSON, _ := opts["bots_json"].(string); botsJSON != "" {
		d.botsJSON = botsJSON
	}

	if !d.started {
		return nil
	}

	bots := d.resolveBotConfigs(opts)
	d.reconcileWorkers(bots)
	return nil
}

func (d *WeixinDialog) startWorker(parent context.Context, cfg BotConfig) {
	ctx, cancel := context.WithCancel(parent)
	w := &BotWorker{
		cfg:    cfg,
		cancel: cancel,
		status: "starting",
		api: &ApiOptions{
			BaseURL:    cfg.BaseURL,
			Token:      cfg.Token,
			LongPoll:   DefaultLongPollTimeout,
			IlinkAppId: "bot",
		},
	}

	d.workerMu.Lock()
	if old, ok := d.workers[cfg.ID]; ok && old.cancel != nil {
		old.cancel()
	}
	d.workers[cfg.ID] = w
	d.workerMu.Unlock()

	d.wg.Add(1)
	go d.pollLoop(ctx, w)
}

func (d *WeixinDialog) pollLoop(ctx context.Context, w *BotWorker) {
	defer d.wg.Done()
	slog.Info("weixin: starting message poll loop", "bot", w.cfg.ID)

	getUpdatesBuf := w.buf
	for {
		select {
		case <-ctx.Done():
			w.status = "stopped"
			return
		default:
			resp, err := w.api.GetUpdates(ctx, &GetUpdatesReq{
				GetUpdatesBuf: getUpdatesBuf,
				BaseInfo:      &BaseInfo{ChannelVersion: "1.0.2"},
			})
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				w.status = "error"
				w.lastErr = err
				slog.Error("weixin: poll error, retrying in 5s", "bot", w.cfg.ID, "error", err)
				time.Sleep(5 * time.Second)
				continue
			}

			if resp.Ret != 0 {
				w.status = "error"
				slog.Error("weixin: api returned error", "bot", w.cfg.ID, "code", resp.Ret, "errcode", resp.ErrCode, "msg", resp.ErrMsg)
				if resp.Ret == -14 || resp.ErrCode == -14 { // session timeout
					w.status = "session_expired"
					slog.Info("weixin: session timeout, wait for re-login", "bot", w.cfg.ID)
					return
				}
				time.Sleep(5 * time.Second)
				continue
			}

			w.status = "connected"
			getUpdatesBuf = resp.GetUpdatesBuf
			w.buf = getUpdatesBuf
			for _, msg := range resp.Msgs {
				// 核心修复：异步处理消息，防止阻塞微信心跳和后续消息接收
				go d.handleIncoming(w, msg)
			}
		}
	}
}

func (d *WeixinDialog) handleIncoming(w *BotWorker, msg *WeixinMessage) {
	if !d.isAllowed(w.cfg.AllowFrom, msg.FromUserID) {
		slog.Debug("weixin: ignoring message from unauthorized user", "user", msg.FromUserID)
		return
	}

	var content strings.Builder
	for _, item := range msg.ItemList {
		if item.Type == MessageItemTypeText && item.TextItem != nil {
			content.WriteString(item.TextItem.Text)
		}
		// Handle other types as needed
	}

	if content.Len() == 0 {
		return
	}

	now := time.Now()
	w.mu.Lock()
	w.lastInboundAt = now
	w.warned22At = time.Time{}
	w.warned23At = time.Time{}
	w.lastContext = msg.ContextToken
	w.lastPeer = msg.FromUserID
	w.lastTokenAt = now
	w.mu.Unlock()

	sessionKey := fmt.Sprintf("weixin:%s:%s", w.cfg.ID, msg.FromUserID)
	if msg.GroupID != "" {
		sessionKey = fmt.Sprintf("weixin:%s:%s:%s", w.cfg.ID, msg.GroupID, msg.FromUserID)
	}

	replyCtx := ReplyContext{
		BotID:        w.cfg.ID,
		ContextToken: msg.ContextToken,
		ToUserID:     msg.FromUserID,
		CreatedAt:    now.Unix(),
	}

	d.handler(d, &agent.Message{
		MessageID:  fmt.Sprintf("%d", msg.MessageID),
		SessionKey: sessionKey,
		UserID:     msg.FromUserID,
		Content:    content.String(),
		CreateTime: time.Unix(msg.CreateTimeMs/1000, 0),
		ReplyCtx:   replyCtx,
	})
}

func (d *WeixinDialog) isAllowed(allowFrom, userID string) bool {
	if allowFrom == "" {
		allowFrom = d.allowFrom
	}
	if allowFrom == "" {
		return true
	}
	allowed := strings.Split(allowFrom, ",")
	for _, a := range allowed {
		if strings.TrimSpace(a) == userID {
			return true
		}
	}
	return false
}

func (d *WeixinDialog) keepaliveLoop(ctx context.Context) {
	defer d.wg.Done()
	ticker := time.NewTicker(keepaliveScanTicker)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.scanKeepalive()
		}
	}
}

func (d *WeixinDialog) scanKeepalive() {
	d.workerMu.RLock()
	workers := make([]*BotWorker, 0, len(d.workers))
	for _, w := range d.workers {
		workers = append(workers, w)
	}
	d.workerMu.RUnlock()

	now := time.Now()
	for _, w := range workers {
		w.mu.Lock()
		if w.status != "connected" || w.lastContext == "" || w.lastPeer == "" || w.lastTokenAt.IsZero() {
			w.mu.Unlock()
			continue
		}

		age := now.Sub(w.lastTokenAt)
		if age >= keepaliveWindow {
			w.status = "session_expired"
			slog.Warn("weixin: context token expired", "bot", w.cfg.ID, "peer", w.lastPeer)
			w.mu.Unlock()
			continue
		}

		shouldWarn22 := age >= keepaliveWarn1 && w.warned22At.IsZero()
		shouldWarn23 := age >= keepaliveWarn2 && w.warned23At.IsZero()
		peer := w.lastPeer
		ctxToken := w.lastContext
		w.mu.Unlock()

		if shouldWarn22 {
			if d.sendKeepaliveReminder(w, peer, ctxToken, 22) == nil {
				w.mu.Lock()
				w.warned22At = now
				w.mu.Unlock()
			}
		}
		if shouldWarn23 {
			if d.sendKeepaliveReminder(w, peer, ctxToken, 23) == nil {
				w.mu.Lock()
				w.warned23At = now
				w.mu.Unlock()
			}
		}
	}
}

func (d *WeixinDialog) sendKeepaliveReminder(w *BotWorker, toUserID, contextToken string, hour int) error {
	text := "[系统提醒] 会话即将过期，请回复任意消息保持会话活跃。"
	if hour >= 23 {
		text = "[系统提醒] 会话将在 1 小时内过期，请尽快回复任意消息避免掉线。"
	}
	req := &SendMessageReq{Msg: &WeixinMessage{
		ToUserID:     toUserID,
		ContextToken: contextToken,
		ItemList: []*MessageItem{{
			Type:     MessageItemTypeText,
			TextItem: &TextItem{Text: text},
		}},
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := w.api.SendMessage(ctx, req)
	if err != nil {
		slog.Warn("weixin: keepalive reminder failed", "bot", w.cfg.ID, "hour", hour, "error", err)
		return err
	}
	slog.Info("weixin: keepalive reminder sent", "bot", w.cfg.ID, "hour", hour)
	return nil
}

func (d *WeixinDialog) parseReplyContext(replyCtx any) (ReplyContext, error) {
	switch v := replyCtx.(type) {
	case ReplyContext:
		return v, nil
	case *ReplyContext:
		if v == nil {
			return ReplyContext{}, fmt.Errorf("weixin: nil reply context")
		}
		return *v, nil
	case string:
		// Backward-compatible path for legacy single-bot context token replies.
		id := d.defaultBotID()
		return ReplyContext{BotID: id, ContextToken: v}, nil
	default:
		return ReplyContext{}, fmt.Errorf("weixin: invalid reply context type")
	}
}

func (d *WeixinDialog) defaultBotID() string {
	d.workerMu.RLock()
	defer d.workerMu.RUnlock()
	if len(d.workers) == 0 {
		return "default"
	}
	ids := make([]string, 0, len(d.workers))
	for id := range d.workers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids[0]
}

func (d *WeixinDialog) requireWorker(botID string) (*BotWorker, error) {
	if botID == "" {
		botID = d.defaultBotID()
	}
	d.workerMu.RLock()
	w, ok := d.workers[botID]
	d.workerMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("weixin: bot %q not running", botID)
	}
	return w, nil
}

func (d *WeixinDialog) resolveBotConfigs(opts map[string]any) []BotConfig {
	botsJSON := d.botsJSON
	if opts != nil {
		if v, _ := opts["bots_json"].(string); v != "" {
			botsJSON = v
		}
	}

	if strings.TrimSpace(botsJSON) != "" {
		var in []botJSONConfig
		if err := json.Unmarshal([]byte(botsJSON), &in); err != nil {
			slog.Error("weixin: parse bots_json failed", "error", err)
			return nil
		}
		cfgs := make([]BotConfig, 0, len(in))
		for _, c := range in {
			if c.Token == "" {
				continue
			}
			id := c.ID
			if id == "" {
				id = c.AccountID
			}
			if id == "" {
				id = fmt.Sprintf("bot-%d", len(cfgs)+1)
			}
			base := c.BaseURL
			if base == "" {
				base = d.baseURL
			}
			cfgs = append(cfgs, BotConfig{
				ID:        id,
				BaseURL:   base,
				Token:     c.Token,
				AccountID: c.AccountID,
				AllowFrom: c.AllowFrom,
			})
		}
		return cfgs
	}

	if d.token == "" {
		return nil
	}
	id := d.accountID
	if id == "" {
		id = "default"
	}
	return []BotConfig{{
		ID:        id,
		BaseURL:   d.baseURL,
		Token:     d.token,
		AccountID: d.accountID,
		AllowFrom: d.allowFrom,
	}}
}

func (d *WeixinDialog) reconcileWorkers(target []BotConfig) {
	targetMap := make(map[string]BotConfig, len(target))
	for _, cfg := range target {
		targetMap[cfg.ID] = cfg
	}

	ctx := context.Background()
	d.workerMu.Lock()
	for id, w := range d.workers {
		cfg, ok := targetMap[id]
		if !ok {
			if w.cancel != nil {
				w.cancel()
			}
			delete(d.workers, id)
			continue
		}
		if w.cfg.Token != cfg.Token || w.cfg.BaseURL != cfg.BaseURL || w.cfg.AllowFrom != cfg.AllowFrom {
			if w.cancel != nil {
				w.cancel()
			}
			delete(d.workers, id)
		}
	}
	d.workerMu.Unlock()

	for _, cfg := range target {
		d.workerMu.RLock()
		_, ok := d.workers[cfg.ID]
		d.workerMu.RUnlock()
		if !ok {
			d.startWorker(ctx, cfg)
		}
	}
}
