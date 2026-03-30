package langdetector

import (
	"context"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "langdetector",
		PluginType:  "pipe",
		Description: "Detects user language and annotates the message",
	})

	agent.RegisterPipe("langdetector", 400, func(_ agent.PipeContext) agent.Pipe {
		return NewLangDetector()
	})
}

// LangDetector identifies the user's language.
type LangDetector struct{}

func NewLangDetector() *LangDetector {
	return &LangDetector{}
}

func (l *LangDetector) Handle(_ context.Context, _ agent.Dialog, _ *agent.Message) bool {
	// Simple placeholder. In production, use a library or AI to detect.
	// For now, we assume it's English if it contains common English words.
	return false
}
