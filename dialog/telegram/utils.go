package telegram

import (
	"log/slog"
	"strings"
	"time"
)

// allowList checks whether a user ID is permitted.
func allowList(allowFrom, userID string) bool {
	allowFrom = strings.TrimSpace(allowFrom)
	if allowFrom == "" || allowFrom == "*" {
		return true
	}
	for _, id := range strings.Split(allowFrom, ",") {
		if strings.EqualFold(strings.TrimSpace(id), userID) {
			return true
		}
	}
	return false
}

// checkAllowFrom logs a security warning.
func checkAllowFrom(access, allowFrom string) {
	if strings.TrimSpace(allowFrom) == "" {
		slog.Warn("allow_from is not set — all users are permitted.", "access", access)
	}
}

// isOldMessage returns true if the message is older than 5 minutes.
func isOldMessage(createTime time.Time) bool {
	return time.Since(createTime) > 5*time.Minute
}

// isValidTelegramCommand validates if a command string meets Telegram's requirements.
// Telegram command rules:
//   - 1-32 characters long
//   - Only lowercase letters, digits, and underscores
//   - Must start with a letter
func isValidTelegramCommand(cmd string) bool {
	if len(cmd) == 0 || len(cmd) > 32 {
		return false
	}
	// Must start with a letter
	if cmd[0] < 'a' || cmd[0] > 'z' {
		return false
	}
	// Rest can be letters, digits, or underscores
	for i := 1; i < len(cmd); i++ {
		c := cmd[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}
