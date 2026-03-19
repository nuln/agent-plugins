package telegram

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/nuln/agent-core"
)

func (p *TelegramAccess) handleCallbackQuery(cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil || cb.From == nil {
		return
	}

	data := cb.Data
	chatID := cb.Message.Chat.ID
	msgID := cb.Message.MessageID
	userID := strconv.FormatInt(cb.From.ID, 10)

	if !allowList(p.allowFrom, userID) {
		slog.Debug("telegram: callback from unauthorized user", "user", userID)
		return
	}

	// Answer the callback to clear the loading indicator
	answer := tgbotapi.NewCallback(cb.ID, "")
	p.mu.RLock()
	_, _ = p.bot.Request(answer)
	p.mu.RUnlock()

	userName := cb.From.UserName
	if userName == "" {
		userName = strings.TrimSpace(cb.From.FirstName + " " + cb.From.LastName)
	}
	var sessionKey string
	if p.shareSessionInChannel {
		sessionKey = fmt.Sprintf("telegram:%d", chatID)
	} else {
		sessionKey = fmt.Sprintf("telegram:%d:%d", chatID, cb.From.ID)
	}
	rctx := replyContext{chatID: chatID, messageID: msgID}

	// Command callbacks (cmd:/lang en, cmd:/mode yolo, etc.)
	if strings.HasPrefix(data, "cmd:") {
		command := strings.TrimPrefix(data, "cmd:")

		// Edit original message: append the chosen option and remove buttons
		origText := cb.Message.Text
		if origText == "" {
			origText = ""
		}
		edit := tgbotapi.NewEditMessageText(chatID, msgID, origText+"\n\n> "+command)
		emptyMarkup := tgbotapi.NewInlineKeyboardMarkup()
		edit.ReplyMarkup = &emptyMarkup
		p.mu.RLock()
		_, _ = p.bot.Send(edit)
		p.mu.RUnlock()

		p.handler(p, &agent.Message{
			SessionKey: sessionKey,
			Access:     "telegram",
			UserID:     userID,
			UserName:   userName,
			Content:    command,
			MessageID:  strconv.Itoa(msgID),
			ReplyCtx:   rctx,
		})
		return
	}

	// AskUserQuestion callbacks (askq:qIdx:optIdx)
	if strings.HasPrefix(data, "askq:") {
		// Extract label from after the last colon for display
		parts := strings.SplitN(data, ":", 3)
		choiceLabel := data
		if len(parts) == 3 {
			// Try to find the option label from the original message buttons
			for _, row := range cb.Message.ReplyMarkup.InlineKeyboard {
				for _, btn := range row {
					if btn.CallbackData != nil && *btn.CallbackData == data {
						choiceLabel = "✅ " + btn.Text
					}
				}
			}
		}

		origText := cb.Message.Text
		if origText == "" {
			origText = "(question)"
		}
		edit := tgbotapi.NewEditMessageText(chatID, msgID, origText+"\n\n"+choiceLabel)
		emptyMarkup := tgbotapi.NewInlineKeyboardMarkup()
		edit.ReplyMarkup = &emptyMarkup
		p.mu.RLock()
		_, _ = p.bot.Send(edit)
		p.mu.RUnlock()

		p.handler(p, &agent.Message{
			SessionKey: sessionKey,
			Access:     "telegram",
			UserID:     userID,
			UserName:   userName,
			Content:    data,
			MessageID:  strconv.Itoa(msgID),
			ReplyCtx:   rctx,
		})
		return
	}

	// Permission callbacks (perm:allow, perm:deny, perm:allow_all)
	var responseText string
	switch data {
	case "perm:allow":
		responseText = "allow"
	case "perm:deny":
		responseText = "deny"
	case "perm:allow_all":
		responseText = "allow all"
	default:
		slog.Debug("telegram: unknown callback data", "data", data)
		return
	}

	choiceLabel := responseText
	switch data {
	case "perm:allow":
		choiceLabel = "✅ Allowed"
	case "perm:deny":
		choiceLabel = "❌ Denied"
	case "perm:allow_all":
		choiceLabel = "✅ Allow All"
	}

	origText := cb.Message.Text
	if origText == "" {
		origText = "(permission request)"
	}
	edit := tgbotapi.NewEditMessageText(chatID, msgID, origText+"\n\n"+choiceLabel)
	emptyMarkup := tgbotapi.NewInlineKeyboardMarkup()
	edit.ReplyMarkup = &emptyMarkup
	p.mu.RLock()
	_, _ = p.bot.Send(edit)
	p.mu.RUnlock()

	p.handler(p, &agent.Message{
		SessionKey: sessionKey,
		Access:     "telegram",
		UserID:     userID,
		UserName:   userName,
		Content:    responseText,
		MessageID:  strconv.Itoa(msgID),
		ReplyCtx:   rctx,
	})
}

// isDirectedAtBot checks whether a group message is directed at this bot:
//   - Command with @thisbot suffix (e.g. /help@thisbot)
//   - Command without @suffix (broadcast to all bots — accept it)
//   - Command with @otherbot suffix → reject
//   - Non-command: accept if bot is @mentioned or message is a reply to bot
func (p *TelegramAccess) isDirectedAtBot(msg *tgbotapi.Message) bool {
	p.mu.RLock()
	botName := p.bot.Self.UserName
	botID := p.bot.Self.ID
	p.mu.RUnlock()

	// Commands: /cmd or /cmd@botname
	if msg.IsCommand() {
		atIdx := strings.Index(msg.Text, "@")
		spaceIdx := strings.Index(msg.Text, " ")
		cmdEnd := len(msg.Text)
		if spaceIdx > 0 {
			cmdEnd = spaceIdx
		}
		if atIdx > 0 && atIdx < cmdEnd {
			target := msg.Text[atIdx+1 : cmdEnd]
			slog.Debug("telegram: command with @suffix", "bot", botName, "target", target, "match", strings.EqualFold(target, botName))
			return strings.EqualFold(target, botName)
		}
		slog.Debug("telegram: command without @suffix, accepting", "bot", botName, "text", msg.Text)
		return true // /cmd without @suffix — accept
	}

	// Non-command: check @mention
	if msg.Entities != nil {
		for _, e := range msg.Entities {
			if e.Type == "mention" && e.Offset+e.Length <= len(msg.Text) {
				mention := msg.Text[e.Offset : e.Offset+e.Length]
				slog.Debug("telegram: checking mention", "bot", botName, "mention", mention, "match", strings.EqualFold(mention, "@"+botName))
				if strings.EqualFold(mention, "@"+botName) {
					return true
				}
			}
		}
	}

	// Check if replying to a message from this bot
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		slog.Debug("telegram: checking reply", "bot_id", botID, "reply_from_id", msg.ReplyToMessage.From.ID)
		if msg.ReplyToMessage.From.ID == botID {
			return true
		}
	}

	// Also check caption entities (for photos with captions)
	if msg.CaptionEntities != nil {
		for _, e := range msg.CaptionEntities {
			if e.Type == "mention" && e.Offset+e.Length <= len(msg.Caption) {
				mention := msg.Caption[e.Offset : e.Offset+e.Length]
				if strings.EqualFold(mention, "@"+botName) {
					return true
				}
			}
		}
	}

	slog.Debug("telegram: ignoring group message not directed at bot", "chat", msg.Chat.ID, "bot", botName, "text", msg.Text, "entities", msg.Entities)
	return false
}

// RegisterCommands registers bot commands with Telegram for the command menu.
func (p *TelegramAccess) RegisterCommands(commands []agent.BotCommandInfo) error {
	if p.bot == nil {
		return fmt.Errorf("telegram: bot not initialized")
	}

	// Telegram limits: max 100 commands, description max 256 chars
	tgCommands := make([]tgbotapi.BotCommand, 0, len(commands))
	for _, c := range commands {
		if !isValidTelegramCommand(c.Command) {
			slog.Warn("telegram: invalid command, skipping",
				slog.String("command", c.Command),
				slog.String("description", c.Description))
			continue
		}
		desc := c.Description
		if len(desc) > 256 {
			desc = desc[:253] + "..."
		}
		tgCommands = append(tgCommands, tgbotapi.BotCommand{
			Command:     c.Command,
			Description: desc,
		})
	}

	// Limit to 100 commands
	if len(tgCommands) > 100 {
		tgCommands = tgCommands[:100]
	}

	if len(tgCommands) == 0 {
		slog.Debug("telegram: no commands to register")
		return nil
	}

	cfg := tgbotapi.NewSetMyCommands(tgCommands...)
	_, err := p.bot.Request(cfg)
	if err != nil {
		return fmt.Errorf("telegram: setMyCommands failed: %w", err)
	}

	slog.Info("telegram: registered bot commands", "count", len(tgCommands))
	return nil
}
