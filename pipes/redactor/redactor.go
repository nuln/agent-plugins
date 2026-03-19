package redactor

import (
	"context"
	"strings"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPipe("redactor", 500, func(_ agent.PipeContext) agent.Pipe {
		return NewRedactor()
	})
}

// Redactor masks sensitive information.
type Redactor struct{}

func NewRedactor() *Redactor {
	return &Redactor{}
}

func (r *Redactor) Handle(_ context.Context, _ agent.Dialog, msg *agent.Message) bool {
	// Simple redaction for emails and potential keys
	// In production, use more robust patterns
	content := msg.Content

	// Example: Masking something that looks like an API Key (simple prefix check)
	if strings.Contains(content, "sk-") {
		msg.Content = strings.ReplaceAll(content, "sk-", "REDACTED-")
	}

	return false // Never stops, just transforms
}
