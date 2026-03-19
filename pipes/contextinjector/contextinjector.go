package contextinjector

import (
	"context"
	"fmt"
	"time"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPipe("contextinjector", 600, func(_ agent.PipeContext) agent.Pipe {
		return NewContextInjector()
	})
}

// ContextInjector adds environment information to the message.
type ContextInjector struct{}

func NewContextInjector() *ContextInjector {
	return &ContextInjector{}
}

func (c *ContextInjector) Handle(_ context.Context, _ agent.Dialog, msg *agent.Message) bool {
	// Inject current time and system info as a prefix or suffix for the AI to see
	msg.Content = fmt.Sprintf("[Time: %s] %s", time.Now().Format("2006-01-02 15:04:05"), msg.Content)
	return false
}
