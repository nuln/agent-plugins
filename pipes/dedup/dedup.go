package dedup

import (
	"context"
	"sync"
	"time"

	"github.com/nuln/agent-core"
)

func init() {
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
	return &MessageDedup{
		seen: make(map[string]time.Time),
	}
}

func (d *MessageDedup) Handle(_ context.Context, _ agent.Dialog, msg *agent.Message) bool {
	return d.IsDuplicate(msg.MessageID)
}

// IsDuplicate returns true if msgID was already seen within the TTL window.
func (d *MessageDedup) IsDuplicate(msgID string) bool {
	if msgID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	// Cleanup old entries
	for k, t := range d.seen {
		if now.Sub(t) > dedupTTL {
			delete(d.seen, k)
		}
	}

	if _, ok := d.seen[msgID]; ok {
		return true
	}
	d.seen[msgID] = now
	return false
}

// IsOldMessage returns true if msgTime is before the process StartTime.
func IsOldMessage(msgTime time.Time) bool {
	return msgTime.Before(StartTime.Add(-2 * time.Second))
}
