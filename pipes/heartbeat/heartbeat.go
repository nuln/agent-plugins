// Package heartbeat provides a pipe that sends periodic prompts to a session.
// It starts a background goroutine that calls PipeContext.Inject at a configured interval.
//
// Configuration (environment variables):
//
//	HEARTBEAT_SESSION_KEY    - session key to inject prompts into (required)
//	HEARTBEAT_PROMPT         - prompt text (default: "Heartbeat check-in: please provide a brief status update.")
//	HEARTBEAT_INTERVAL_MINS  - interval in minutes (default: 30)
//
// Auto-registration: import _ "github.com/nuln/agent-plugins/pipes/heartbeat"
package heartbeat

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	agent "github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "heartbeat",
		PluginType:  "pipe",
		Description: "Periodic prompt injection into a session",
		Fields: []agent.ConfigField{
			{EnvVar: "HEARTBEAT_SESSION_KEY", Key: "session_key", Description: "Session key to inject prompts into", Required: true, Type: agent.ConfigFieldString},
			{EnvVar: "HEARTBEAT_PROMPT", Key: "prompt", Description: "Prompt text to inject", Default: "Heartbeat check-in: please provide a brief status update.", Type: agent.ConfigFieldString},
			{EnvVar: "HEARTBEAT_INTERVAL_MINS", Key: "interval_mins", Description: "Interval in minutes between prompts", Default: "30", Type: agent.ConfigFieldInt, Example: "30"},
		},
	})

	agent.RegisterPipe("heartbeat", 40, func(pctx agent.PipeContext) agent.Pipe {
		sessionKey := os.Getenv("HEARTBEAT_SESSION_KEY")
		prompt := os.Getenv("HEARTBEAT_PROMPT")
		if prompt == "" {
			prompt = "Heartbeat check-in: please provide a brief status update."
		}
		intervalMins := 30
		if v := os.Getenv("HEARTBEAT_INTERVAL_MINS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				intervalMins = n
			}
		}
		p := &heartbeatPipe{
			pctx:         pctx,
			sessionKey:   sessionKey,
			prompt:       prompt,
			intervalMins: intervalMins,
			stopCh:       make(chan struct{}),
		}
		if sessionKey != "" {
			go p.run()
		} else {
			slog.Warn("heartbeat: HEARTBEAT_SESSION_KEY not set, heartbeat disabled")
		}
		return p
	})
}

type heartbeatPipe struct {
	pctx         agent.PipeContext
	sessionKey   string
	prompt       string
	intervalMins int
	stopCh       chan struct{}
	mu           sync.Mutex
	ticker       *time.Ticker
}

// Handle is a no-op passthrough; heartbeat works asynchronously.
func (p *heartbeatPipe) Handle(_ context.Context, _ agent.Dialog, _ *agent.Message) bool {
	return false
}

func (p *heartbeatPipe) run() {
	interval := time.Duration(p.intervalMins) * time.Minute
	ticker := time.NewTicker(interval)
	p.mu.Lock()
	p.ticker = ticker
	p.mu.Unlock()
	defer ticker.Stop()
	slog.Info("heartbeat: started",
		"session_key", p.sessionKey,
		"interval_mins", p.intervalMins,
	)
	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.fire()
		}
	}
}

func (p *heartbeatPipe) fire() {
	if p.pctx.Inject == nil {
		return
	}
	slog.Debug("heartbeat: firing", "session_key", p.sessionKey)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	p.pctx.Inject(ctx, p.sessionKey, p.prompt)
}

// SetInterval changes the heartbeat interval at runtime.
func (p *heartbeatPipe) SetInterval(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ticker != nil {
		p.ticker.Reset(d)
	}
}

// Stop terminates the background heartbeat goroutine.
func (p *heartbeatPipe) Stop() {
	select {
	case <-p.stopCh:
	default:
		close(p.stopCh)
	}
}
