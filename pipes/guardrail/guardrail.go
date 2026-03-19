package guardrail

import (
	"context"
	"strings"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPipe("guardrail", 700, func(ctx agent.PipeContext) agent.Pipe {
		// Admin list could be loaded from config here
		return NewGuardrail([]string{"admin"}, ctx.Sessions, ctx.Translator)
	})
}

// Guardrail handles safety checks and permissions.
type Guardrail struct {
	Admins   []string
	sessions agent.SessionProvider
	i18n     agent.Translator
}

func NewGuardrail(admins []string, sessions agent.SessionProvider, i18n agent.Translator) *Guardrail {
	return &Guardrail{
		Admins:   admins,
		sessions: sessions,
		i18n:     i18n,
	}
}

func (g *Guardrail) Handle(ctx context.Context, p agent.Dialog, msg *agent.Message) bool {
	session := g.sessions.GetOrCreateActive(msg.SessionKey)

	// 1. Check pending actions
	if session.GetPendingAction() == "DESTRUCTIVE_OP" {
		if strings.ToUpper(msg.Content) == "YES" {
			session.SetPendingAction("") // Clear state
			return false                 // Continue to llm
		}
		_ = p.Reply(ctx, msg.ReplyCtx, "Operation cancelled.")
		session.SetPendingAction("")
		return true // Blocked
	}

	// 2. Check destructive intent
	if g.DetectDestructiveIntent(msg.Content) && !g.IsAdmin(msg.UserID) {
		session.SetPendingAction("DESTRUCTIVE_OP")
		_ = p.Reply(ctx, msg.ReplyCtx, "Warning: Potential destructive operation detected. Reply YES to confirm.")
		return true // Blocked for confirmation
	}

	return false
}

// IsAdmin checks if a user is an administrator.
func (g *Guardrail) IsAdmin(userID string) bool {
	for _, a := range g.Admins {
		if a == userID {
			return true
		}
	}
	return false
}

// CheckCommand checks if a command is sensitive and if the user has permission.
func (g *Guardrail) IsSensitiveCommand(cmdName string) bool {
	sensitive := []string{"reset", "delete", "wipe", "system", "exec"}
	cmdName = strings.ToLower(cmdName)
	for _, s := range sensitive {
		if strings.Contains(cmdName, s) {
			return true
		}
	}
	return false
}

// SanitizeInput cleans input to prevent injection.
func (g *Guardrail) SanitizeInput(input string) string {
	// Basic protection against shell injection characters if used in templates
	danger := []string{";", "&&", "||", "`", "$(", "|", ">", "<"}
	for _, d := range danger {
		input = strings.ReplaceAll(input, d, "")
	}
	return input
}

// DetectDestructiveIntent finds phrases that might mean deleting data.
func (g *Guardrail) DetectDestructiveIntent(text string) bool {
	keywords := []string{"delete all", "wipe", "destroy", "clear"}
	text = strings.ToLower(text)
	for _, k := range keywords {
		if strings.Contains(text, k) {
			return true
		}
	}
	return false
}
