package telemetry

import (
	"context"
	"log/slog"
	"time"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "telemetry",
		PluginType:  "pipe",
		Description: "Logs message metadata for audit and metrics",
	})

	agent.RegisterPipe("telemetry", 100, func(_ agent.PipeContext) agent.Pipe {
		return NewTelemetry()
	})
}

// Telemetry implements both Audit and Metrics functionalities.
type Telemetry struct {
	// In a real app, this might have a stats aggregator or DB logger
}

func NewTelemetry() *Telemetry {
	return &Telemetry{}
}

func (t *Telemetry) Handle(_ context.Context, _ agent.Dialog, msg *agent.Message) bool {
	slog.Info("Message received",
		"id", msg.MessageID,
		"access", msg.Access,
		"user", msg.UserID,
		"len", len(msg.Content),
		"time", time.Now().Format(time.RFC3339),
	)
	// This interceptor never stops the chain
	return false
}
