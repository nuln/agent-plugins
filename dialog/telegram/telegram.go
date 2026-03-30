package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nuln/agent-core"

	"regexp"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var (
	reHeading        = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	reHorizontal     = regexp.MustCompile(`(?m)^---+\s*$`)
	reInlineCodeHTML = regexp.MustCompile("`([^`]+)`")
	reBoldAstHTML    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUndHTML    = regexp.MustCompile(`__(.+?)__`)
	reItalicAstHTML  = regexp.MustCompile(`(?:^|[^*])\*([^*]+?)\*(?:[^*]|$)`)
	reStrikeHTML     = regexp.MustCompile(`~~(.+?)~~`)
	reLinkHTML       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "telegram",
		PluginType:  "dialog",
		Description: "Telegram Bot messaging platform integration",
		Fields: []agent.ConfigField{
			{EnvVar: "TELEGRAM_TOKEN", Key: "token", Description: "Telegram Bot API token", Required: true, Type: agent.ConfigFieldSecret},
			{EnvVar: "TELEGRAM_ALLOW_USER_IDS", Key: "allow_from", Description: "Comma-separated list of allowed Telegram user IDs", Type: agent.ConfigFieldString},
			{Key: "proxy", Description: "HTTP proxy URL for Telegram API access", Type: agent.ConfigFieldString},
			{Key: "proxy_username", Description: "Proxy authentication username", Type: agent.ConfigFieldString},
			{Key: "proxy_password", Description: "Proxy authentication password", Type: agent.ConfigFieldSecret},
			{Key: "group_reply_all", Description: "Reply to all messages in group chats", Default: "false", Type: agent.ConfigFieldBool},
			{Key: "share_session_in_channel", Description: "Share session across group channel", Default: "false", Type: agent.ConfigFieldBool},
		},
	})

	agent.RegisterDialog("telegram", func(opts map[string]any) (agent.Dialog, error) {
		return New(opts)
	})
}

type replyContext struct {
	chatID    int64
	messageID int
}

type TelegramAccess struct {
	token                 string
	allowFrom             string
	groupReplyAll         bool
	shareSessionInChannel bool
	bot                   *tgbotapi.BotAPI
	httpClient            *http.Client
	handler               agent.MessageHandler
	cancel                context.CancelFunc
	mu                    sync.RWMutex
}

func New(opts map[string]any) (agent.Dialog, error) {
	token, _ := opts["token"].(string)
	if token == "" {
		token = os.Getenv("TELEGRAM_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("telegram: token is required")
	}
	allowFrom, _ := opts["allow_from"].(string)
	if allowFrom == "" {
		allowFrom = os.Getenv("TELEGRAM_ALLOW_USER_IDS")
	}
	checkAllowFrom("telegram", allowFrom)

	// Build HTTP client with optional proxy support
	httpClient := &http.Client{Timeout: 60 * time.Second}
	if proxyURL, _ := opts["proxy"].(string); proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("telegram: invalid proxy URL %q: %w", proxyURL, err)
		}
		proxyUser, _ := opts["proxy_username"].(string)
		proxyPass, _ := opts["proxy_password"].(string)
		if proxyUser != "" {
			u.User = url.UserPassword(proxyUser, proxyPass)
		}
		httpClient.Transport = &http.Transport{Proxy: http.ProxyURL(u)}
		slog.Info("telegram: using proxy", "proxy", u.Host, "auth", proxyUser != "")
	}

	groupReplyAll, _ := opts["group_reply_all"].(bool)
	shareSessionInChannel, _ := opts["share_session_in_channel"].(bool)
	return &TelegramAccess{token: token, allowFrom: allowFrom, groupReplyAll: groupReplyAll, shareSessionInChannel: shareSessionInChannel, httpClient: httpClient}, nil
}

func (p *TelegramAccess) Name() string { return "telegram" }

func (p *TelegramAccess) Start(handler agent.MessageHandler) error {
	p.handler = handler

	bot, err := tgbotapi.NewBotAPIWithClient(p.token, tgbotapi.APIEndpoint, p.httpClient)
	if err != nil {
		return fmt.Errorf("telegram: auth failed: %w", err)
	}

	p.mu.Lock()
	p.bot = bot
	p.mu.Unlock()

	slog.Info("telegram: authenticated", "user", bot.Self.UserName)

	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	p.mu.RLock()
	updates := p.bot.GetUpdatesChan(u)
	p.mu.RUnlock()

	go func() {
		for {
			select {
			case <-ctx.Done():
				slog.Debug("telegram: update loop stopped")
				return
			case update := <-updates:
				if update.CallbackQuery != nil {
					p.handleCallbackQuery(update.CallbackQuery)
				}

				if update.Message == nil {
					continue
				}

				msg := update.Message

				// Skip old messages (older than 5 minutes)
				if isOldMessage(msg.Time()) {
					slog.Debug("telegram: ignoring old message", "age", time.Since(msg.Time()).String())
					continue
				}

				// Skip if from bot itself
				p.mu.RLock()
				botID := p.bot.Self.ID
				p.mu.RUnlock()
				if msg.From != nil && msg.From.ID == botID {
					continue
				}

				// Group handling
				if msg.Chat.IsGroup() || msg.Chat.IsSuperGroup() {
					if !p.isDirectedAtBot(msg) {
						continue
					}
				}

				// User authorization
				userID := strconv.FormatInt(msg.From.ID, 10)
				if !allowList(p.allowFrom, userID) {
					slog.Debug("telegram: message from unauthorized user", "user", userID)
					continue
				}

				// Determine session key
				var sessionKey string
				if p.shareSessionInChannel {
					sessionKey = fmt.Sprintf("telegram:%d", msg.Chat.ID)
				} else {
					sessionKey = fmt.Sprintf("telegram:%d:%d", msg.Chat.ID, msg.From.ID)
				}

				// User name
				userName := msg.From.UserName
				if userName == "" {
					userName = strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName)
				}

				// Prepare reply context
				rctx := replyContext{chatID: msg.Chat.ID, messageID: msg.MessageID}

				// Remove bot mention from text if in group
				text := msg.Text
				p.mu.RLock()
				botUserName := p.bot.Self.UserName
				p.mu.RUnlock()
				if botUserName != "" {
					text = strings.ReplaceAll(text, "@"+botUserName, "")
					text = strings.TrimSpace(text)
				}

				coreMsg := &agent.Message{
					SessionKey: sessionKey, Access: "telegram",
					UserID: userID, UserName: userName,
					Content:   text,
					MessageID: strconv.Itoa(msg.MessageID),
					ReplyCtx:  rctx,
				}

				slog.Debug("telegram: message received", "user", userName, "chat", msg.Chat.ID)
				p.handler(p, coreMsg)
			}
		}
	}()

	return nil
}

func (p *TelegramAccess) Reply(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context")
	}

	msg := tgbotapi.NewMessage(rc.chatID, MarkdownToSimpleHTML(content))
	msg.ParseMode = tgbotapi.ModeHTML
	msg.ReplyToMessageID = rc.messageID

	p.mu.RLock()
	_, err := p.bot.Send(msg)
	p.mu.RUnlock()

	return err
}

func (p *TelegramAccess) Send(ctx context.Context, rctx any, content string) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context")
	}

	msg := tgbotapi.NewMessage(rc.chatID, MarkdownToSimpleHTML(content))
	msg.ParseMode = tgbotapi.ModeHTML

	p.mu.RLock()
	_, err := p.bot.Send(msg)
	p.mu.RUnlock()

	return err
}

func (p *TelegramAccess) SendWithButtons(ctx context.Context, rctx any, content string, buttons [][]agent.ButtonOption) error {
	rc, ok := rctx.(replyContext)
	if !ok {
		return fmt.Errorf("telegram: invalid reply context")
	}

	msg := tgbotapi.NewMessage(rc.chatID, MarkdownToSimpleHTML(content))
	msg.ParseMode = tgbotapi.ModeHTML

	if len(buttons) > 0 {
		var rows [][]tgbotapi.InlineKeyboardButton
		for _, row := range buttons {
			var btns []tgbotapi.InlineKeyboardButton
			for _, b := range row {
				btns = append(btns, tgbotapi.NewInlineKeyboardButtonData(b.Text, b.Data))
			}
			rows = append(rows, btns)
		}
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	}

	p.mu.RLock()
	_, err := p.bot.Send(msg)
	p.mu.RUnlock()

	return err
}

func (p *TelegramAccess) DeletePreviewMessage(ctx context.Context, previewHandle any) error {
	handle, ok := previewHandle.(telegramPreviewHandle)
	if !ok {
		return fmt.Errorf("telegram: invalid preview handle")
	}

	p.mu.RLock()
	del := tgbotapi.DeleteMessageConfig{
		ChatID:    handle.chatID,
		MessageID: handle.messageID,
	}
	_, err := p.bot.Request(del)
	p.mu.RUnlock()

	return err
}

func (p *TelegramAccess) ReconstructReplyCtx(sessionKey string) (any, error) {
	// Parse sessionKey: telegram:chatID or telegram:chatID:userID
	parts := strings.Split(sessionKey, ":")
	if len(parts) < 2 {
		return nil, fmt.Errorf("telegram: invalid session key: %s", sessionKey)
	}

	chatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("telegram: invalid chat ID: %w", err)
	}

	return replyContext{chatID: chatID, messageID: 0}, nil
}

type telegramPreviewHandle struct {
	chatID    int64
	messageID int
}

func (p *TelegramAccess) SendPreviewStart(ctx context.Context, rctx any, content string) (any, error) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return nil, fmt.Errorf("telegram: invalid reply context")
	}

	msg := tgbotapi.NewMessage(rc.chatID, MarkdownToSimpleHTML(content))
	msg.ParseMode = tgbotapi.ModeHTML

	p.mu.RLock()
	sent, err := p.bot.Send(msg)
	p.mu.RUnlock()

	if err != nil {
		return nil, err
	}

	return telegramPreviewHandle{chatID: sent.Chat.ID, messageID: sent.MessageID}, nil
}

func (p *TelegramAccess) UpdateMessage(ctx context.Context, previewHandle any, content string) error {
	handle, ok := previewHandle.(telegramPreviewHandle)
	if !ok {
		return fmt.Errorf("telegram: invalid preview handle")
	}

	edit := tgbotapi.NewEditMessageText(handle.chatID, handle.messageID, MarkdownToSimpleHTML(content))
	edit.ParseMode = tgbotapi.ModeHTML

	p.mu.RLock()
	_, err := p.bot.Request(edit)
	p.mu.RUnlock()

	return err
}

// StartTyping sends a "typing…" chat action and repeats every 5 seconds
// until the returned stop function is called.
func (p *TelegramAccess) StartTyping(ctx context.Context, rctx any) (stop func()) {
	rc, ok := rctx.(replyContext)
	if !ok {
		return func() {}
	}

	action := tgbotapi.NewChatAction(rc.chatID, tgbotapi.ChatTyping)
	p.mu.RLock()
	_, _ = p.bot.Send(action)
	p.mu.RUnlock()

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.mu.RLock()
				_, _ = p.bot.Send(action)
				p.mu.RUnlock()
			}
		}
	}()

	return func() { close(done) }
}

func (p *TelegramAccess) Stop() error {
	if p.cancel != nil {
		p.cancel()
	}
	p.mu.RLock()
	if p.bot != nil {
		p.bot.StopReceivingUpdates()
	}
	p.mu.RUnlock()
	return nil
}

func (p *TelegramAccess) Reload(opts map[string]any) error {
	var token string
	if opts != nil {
		token, _ = opts["token"].(string)
	}
	if token == "" {
		token = os.Getenv("TELEGRAM_TOKEN")
	}
	if token == "" {
		p.mu.RLock()
		token = p.token
		p.mu.RUnlock()
	}

	p.mu.RLock()
	oldToken := p.token
	p.mu.RUnlock()

	if token == oldToken {
		return nil
	}

	// Validate new token
	newBot, err := tgbotapi.NewBotAPIWithClient(token, tgbotapi.APIEndpoint, p.httpClient)
	if err != nil {
		return fmt.Errorf("telegram reload: validation failed: %w", err)
	}

	slog.Info("telegram: token verified, reloading...", "user", newBot.Self.UserName)

	p.mu.Lock()
	p.token = token
	p.bot = newBot
	p.mu.Unlock()

	// For Telegram, we MUST restart the long polling loop because the updates chan is bound to the bot/token
	if p.cancel != nil {
		p.cancel() // This stops the current loop
	}

	// Restart loop - we need to wait a bit or ensure the old one is gone.
	// In a simple way, we just call Start again if we have a handler
	if p.handler != nil {
		go func() {
			_ = p.Start(p.handler)
		}()
	}

	return nil
}
