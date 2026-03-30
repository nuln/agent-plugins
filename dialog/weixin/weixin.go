package weixin

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

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
			{Key: "allow_from", Description: "Comma-separated list of allowed user IDs", Type: agent.ConfigFieldString},
		},
	})

	agent.RegisterDialog("weixin", func(opts map[string]any) (agent.Dialog, error) {
		return New(opts)
	})
}

type WeixinDialog struct {
	name      string
	baseURL   string
	token     string
	accountID string
	allowFrom string
	api       *ApiOptions
	handler   agent.MessageHandler
	cancel    context.CancelFunc
	wg        sync.WaitGroup
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

	return &WeixinDialog{
		name:      "weixin",
		baseURL:   baseURL,
		token:     token,
		accountID: accountID,
		allowFrom: allowFrom,
		api: &ApiOptions{
			BaseURL:  baseURL,
			Token:    token,
			LongPoll: DefaultLongPollTimeout,
		},
	}, nil
}

func (d *WeixinDialog) Name() string { return d.name }

func (d *WeixinDialog) Start(handler agent.MessageHandler) error {
	d.handler = handler
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel

	if d.token == "" {
		slog.Warn("weixin: no token found, please login via 'ai-agent login weixin'")
		// In a real implementation, we might want to trigger login flow here
		// but typically it's done via a CLI command.
		return nil
	}

	d.wg.Add(1)
	go d.pollLoop(ctx)

	return nil
}

func (d *WeixinDialog) Stop() error {
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
	return nil
}

func (d *WeixinDialog) Reply(ctx context.Context, replyCtx any, content string) error {
	rctx, ok := replyCtx.(string) // In weixin, replyCtx is the context_token
	if !ok {
		return fmt.Errorf("weixin: invalid reply context")
	}

	req := &SendMessageReq{
		Msg: &WeixinMessage{
			ContextToken: rctx,
			ItemList: []*MessageItem{
				{
					Type:     MessageItemTypeText,
					TextItem: &TextItem{Text: content},
				},
			},
		},
	}

	return d.api.SendMessage(ctx, req)
}

func (d *WeixinDialog) Send(ctx context.Context, replyCtx any, content string) error {
	// For Weixin, Send is mostly the same as Reply if we have a context_token.
	// If replyCtx is a userID instead of context_token, we might need a different approach,
	// but the interface's replyCtx is typically what was passed in msg.ReplyCtx.
	return d.Reply(ctx, replyCtx, content)
}

func (d *WeixinDialog) Reload(opts map[string]any) error {
	token, _ := opts["token"].(string)
	if token != "" {
		d.token = token
		d.api.Token = token
	}
	allowFrom, _ := opts["allow_from"].(string)
	if allowFrom != "" {
		d.allowFrom = allowFrom
	}
	return nil
}

func (d *WeixinDialog) pollLoop(ctx context.Context) {
	defer d.wg.Done()
	slog.Info("weixin: starting message poll loop")

	getUpdatesBuf := ""
	for {
		select {
		case <-ctx.Done():
			return
		default:
			resp, err := d.api.GetUpdates(ctx, &GetUpdatesReq{
				GetUpdatesBuf: getUpdatesBuf,
				BaseInfo:      &BaseInfo{ChannelVersion: "1.0.0"},
			})
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Error("weixin: poll error, retrying in 5s", "error", err)
				time.Sleep(5 * time.Second)
				continue
			}

			if resp.Ret != 0 {
				slog.Error("weixin: api returned error", "code", resp.Ret, "msg", resp.ErrMsg)
				if resp.Ret == -14 { // Session timeout
					slog.Info("weixin: session timeout, please re-login")
					return
				}
				time.Sleep(5 * time.Second)
				continue
			}

			getUpdatesBuf = resp.GetUpdatesBuf
			for _, msg := range resp.Msgs {
				d.handleIncoming(msg)
			}
		}
	}
}

func (d *WeixinDialog) handleIncoming(msg *WeixinMessage) {
	if !d.isAllowed(msg.FromUserID) {
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

	sessionKey := fmt.Sprintf("weixin:%s", msg.FromUserID)
	if msg.GroupID != "" {
		sessionKey = fmt.Sprintf("weixin:%s:%s", msg.GroupID, msg.FromUserID)
	}

	d.handler(d, &agent.Message{
		MessageID:  fmt.Sprintf("%d", msg.MessageID),
		SessionKey: sessionKey,
		UserID:     msg.FromUserID,
		Content:    content.String(),
		CreateTime: time.Unix(msg.CreateTimeMs/1000, 0),
		ReplyCtx:   msg.ContextToken,
	})
}

func (d *WeixinDialog) isAllowed(userID string) bool {
	if d.allowFrom == "" {
		return true
	}
	allowed := strings.Split(d.allowFrom, ",")
	for _, a := range allowed {
		if strings.TrimSpace(a) == userID {
			return true
		}
	}
	return false
}
