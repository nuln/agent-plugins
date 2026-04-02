package redactor

import (
	"context"
	"regexp"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "redactor",
		PluginType:  "pipe",
		Description: "Masks sensitive values (API keys, tokens) in message content",
	})

	agent.RegisterPipe("redactor", 500, func(_ agent.PipeContext) agent.Pipe {
		return NewRedactor()
	})
}

// apiKeyRe matches common secret key patterns (sk-, sk_live_, ghp_, etc.)
// followed by at least 20 alphanumeric / dash / underscore characters.
var apiKeyRe = regexp.MustCompile(`\b(sk-[A-Za-z0-9_\-]{20,}|sk_live_[A-Za-z0-9_]{10,}|sk_test_[A-Za-z0-9_]{10,}|ghp_[A-Za-z0-9]{20,}|gho_[A-Za-z0-9]{20,})`)

// Redactor masks sensitive information.
type Redactor struct{}

func NewRedactor() *Redactor {
	return &Redactor{}
}

func (r *Redactor) Handle(_ context.Context, _ agent.Dialog, msg *agent.Message) bool {
	msg.Content = apiKeyRe.ReplaceAllString(msg.Content, "[REDACTED]")
	return false // Never stops, just transforms
}
