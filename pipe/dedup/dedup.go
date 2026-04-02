package dedup

import (
	"context"
	"sync"
	"time"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "dedup",
		PluginType:  "pipe",
		Description: "Deduplicates messages by ID and startup-time filtering",
	})

	agent.RegisterPipe("dedup", 200, func(_ agent.PipeContext) agent.Pipe {
		return NewMessageDedup()
	})
}

const dedupTTL = 60 * time.Second

// StartTime is set once at process startup.
var StartTime = time.Now()

// MessageDedup tracks recently seen message IDs to prevent duplicate processing.
type MessageDedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func NewMessageDedup() *MessageDedup {
	d := &MessageDedup{
		seen: make(map[string]time.Time),
	}
	go d.cleanupLoop()
	return d
}

func (d *MessageDedup) cleanupLoop() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		d.mu.Lock()
		now := time.Now()
		for k, t := range d.seen {
			if now.Sub(t) > dedupTTL {
				delete(d.seen, k)
			}
		}
		d.mu.Unlock()
	}
}

func (d *MessageDedup) Handle(_ context.Context, _ agent.Dialog, msg *agent.Message) bool {
	// If the platform provides a message timestamp, reject messages
	// that pre-date the current process start (startup-time dedup).
	if !msg.CreateTime.IsZero() && IsBeforeProcessStart(msg.CreateTime) {
		return true
	}
	return d.IsDuplicate(msg.MessageID)
}

// IsDuplicate returns true if msgID was already seen within the TTL window.
func (d *MessageDedup) IsDuplicate(msgID string) bool {
	if msgID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.seen[msgID]; ok {
		return true
	}
	d.seen[msgID] = time.Now()
	return false
}

// IsBeforeProcessStart returns true if msgTime predates the current process start.
// A 2-second grace period avoids race conditions with messages sent during startup.
func IsBeforeProcessStart(msgTime time.Time) bool {
	return msgTime.Before(StartTime.Add(-2 * time.Second))
}
