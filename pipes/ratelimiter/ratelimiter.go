package audit

import (
	"context"
	"sync"
	"time"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPipe("ratelimiter", 300, func(_ agent.PipeContext) agent.Pipe {
		return NewRateLimiter()
	})
}

// RateLimiter prevents abuse by limiting message frequency.
type RateLimiter struct {
	mu     sync.Mutex
	limits map[string]time.Time
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		limits: make(map[string]time.Time),
	}
}

func (r *RateLimiter) Handle(ctx context.Context, p agent.Dialog, msg *agent.Message) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	last, ok := r.limits[msg.UserID]
	if ok && time.Since(last) < 2*time.Second { // 2s per message limit
		_ = p.Reply(ctx, msg.ReplyCtx, "Warning: Request too fast, please try again later.")
		return true // Blocked
	}

	r.limits[msg.UserID] = time.Now()
	return false
}
