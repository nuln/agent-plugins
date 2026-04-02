package ratelimiter

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "ratelimiter",
		PluginType:  "pipe",
		Description: "Per-user sliding-window rate limiter",
		Fields: []agent.ConfigField{
			{EnvVar: "RATELIMIT_MAX_MESSAGES", Key: "max_messages", Description: "Maximum messages per window", Default: "20", Type: agent.ConfigFieldInt, Example: "20"},
			{EnvVar: "RATELIMIT_WINDOW_SECS", Key: "window_secs", Description: "Window duration in seconds", Default: "60", Type: agent.ConfigFieldInt, Example: "60"},
		},
	})

	agent.RegisterPipe("ratelimiter", 300, func(_ agent.PipeContext) agent.Pipe {
		maxMsg := 20
		if v := os.Getenv("RATELIMIT_MAX_MESSAGES"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				maxMsg = n
			}
		}
		windowSecs := 60
		if v := os.Getenv("RATELIMIT_WINDOW_SECS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				windowSecs = n
			}
		}
		return NewRateLimiter(maxMsg, time.Duration(windowSecs)*time.Second)
	})
}

// RateLimiter prevents abuse by implementing a per-user sliding-window rate limit.
type RateLimiter struct {
	mu          sync.Mutex
	buckets     map[string]*rateBucket
	maxMessages int
	windowMs    int64
	stopCh      chan struct{}
}

type rateBucket struct {
	timestamps []int64
	lastAccess int64
}

// NewRateLimiter creates a rate limiter allowing maxMessages per window per user.
func NewRateLimiter(maxMessages int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		buckets:     make(map[string]*rateBucket),
		maxMessages: maxMessages,
		windowMs:    window.Milliseconds(),
		stopCh:      make(chan struct{}),
	}
	if maxMessages > 0 {
		go rl.cleanupLoop()
	}
	return rl
}

// Stop terminates the background cleanup goroutine.
func (rl *RateLimiter) Stop() {
	select {
	case <-rl.stopCh:
	default:
		close(rl.stopCh)
	}
}

func (rl *RateLimiter) Handle(ctx context.Context, p agent.Dialog, msg *agent.Message) bool {
	if msg.UserID == "" {
		return false
	}
	if !rl.allow(msg.UserID) {
		_ = p.Reply(ctx, msg.ReplyCtx, "⏳ Rate limit reached. Please wait before sending another message.")
		return true
	}
	return false
}

// allow returns true if the user is within their rate limit.
func (rl *RateLimiter) allow(key string) bool {
	if rl.maxMessages <= 0 {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now().UnixMilli()
	b := rl.buckets[key]
	if b == nil {
		b = &rateBucket{}
		rl.buckets[key] = b
	}
	b.lastAccess = now

	cutoff := now - rl.windowMs
	filtered := b.timestamps[:0]
	for _, ts := range b.timestamps {
		if ts > cutoff {
			filtered = append(filtered, ts)
		}
	}
	b.timestamps = filtered

	if len(b.timestamps) >= rl.maxMessages {
		return false
	}
	b.timestamps = append(b.timestamps, now)
	return true
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			rl.mu.Lock()
			now := time.Now().UnixMilli()
			stale := rl.windowMs * 2
			for k, b := range rl.buckets {
				if now-b.lastAccess > stale {
					delete(rl.buckets, k)
				}
			}
			rl.mu.Unlock()
		}
	}
}
