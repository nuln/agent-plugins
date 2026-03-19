package command

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPipe("command", 1000, func(ctx agent.PipeContext) agent.Pipe {
		// Guardrail is now in command package too
		safety := NewGuardrail([]string{"admin"}, ctx.Sessions, ctx.Translator)
		return NewCommandRegistry(safety)
	})
}

// CustomCommand represents a registered slash command.
type CustomCommand struct {
	Name        string
	Description string
	Prompt      string // template with {{1}}, {{args}}, etc.
}

// CommandRegistry holds all available commands.
type CommandRegistry struct {
	mu       sync.RWMutex
	commands map[string]*CustomCommand
	safety   *Guardrail
}

func NewCommandRegistry(safety *Guardrail) *CommandRegistry {
	return &CommandRegistry{
		commands: make(map[string]*CustomCommand),
		safety:   safety,
	}
}

func (r *CommandRegistry) Handle(ctx context.Context, p agent.Dialog, msg *agent.Message) bool {
	if !strings.HasPrefix(msg.Content, "/") {
		return false
	}

	parts := strings.Fields(msg.Content[1:])
	if len(parts) == 0 {
		return false
	}

	cmdName := parts[0]
	if r.safety.IsSensitiveCommand(cmdName) && !r.safety.IsAdmin(msg.UserID) {
		_ = p.Reply(ctx, msg.ReplyCtx, "Error: You do not have permission to run this command.")
		return true // Blocked
	}

	// Logic for resolving and executing commands...
	// If it's a known command, handle it and return true.
	if _, ok := r.Resolve(cmdName); ok {
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Executing command: %s", cmdName))
		return true
	}

	return false
}

func (r *CommandRegistry) Add(cmd *CustomCommand) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands[strings.ToLower(cmd.Name)] = cmd
}

func (r *CommandRegistry) Resolve(name string) (*CustomCommand, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.commands[strings.ToLower(name)]
	return c, ok
}

var placeholderRe = regexp.MustCompile(`\{\{(\d+\*?|args)(:[^}]*)?\}\}`)

// ExpandPrompt replaces template placeholders with the provided arguments.
func ExpandPrompt(template string, args []string) string {
	if !placeholderRe.MatchString(template) {
		if len(args) > 0 {
			return template + "\n\n" + strings.Join(args, " ")
		}
		return template
	}

	return placeholderRe.ReplaceAllStringFunc(template, func(match string) string {
		inner := match[2 : len(match)-2]
		key, defaultVal, hasDefault := strings.Cut(inner, ":")

		if key == "args" {
			if len(args) > 0 {
				return strings.Join(args, " ")
			}
			if hasDefault {
				return defaultVal
			}
			return ""
		}
		if strings.HasSuffix(key, "*") {
			var idx int
			_, _ = fmt.Sscanf(key, "%d", &idx)
			if idx >= 1 && idx-1 < len(args) {
				return strings.Join(args[idx-1:], " ")
			}
			if hasDefault {
				return defaultVal
			}
			return ""
		}
		var idx int
		_, _ = fmt.Sscanf(key, "%d", &idx)
		if idx >= 1 && idx-1 < len(args) {
			return args[idx-1]
		}
		if hasDefault {
			return defaultVal
		}
		return ""
	})
}

// Guardrail handles safety checks and permissions (embedded for command registry).
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

// IsSensitiveCommand checks if a command is sensitive.
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
