// Package userroles provides a pipe that enforces per-user rate limits and
// command restrictions based on configurable roles.
//
// There is no auto-registration. Callers must configure a UserRoleManager and
// register the pipe manually:
//
//	mgr := userroles.NewUserRoleManager()
//	mgr.Configure(defaultRole, roles)
//	agent.RegisterPipe("userroles", 250, func(pctx agent.PipeContext) agent.Pipe {
//	    return userroles.NewPipe(pctx, mgr)
//	})
package userroles

import (
	"context"
	"strings"
	"sync"
	"time"

	agent "github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "userroles",
		PluginType:  "pipe",
		Description: "Role-based per-user rate limiting and command restrictions (programmatic config only)",
	})
}

// RateLimitCfg configures a sliding-window rate limit.
type RateLimitCfg struct {
	MaxMessages int
	Window      time.Duration
}

// rateLimiter implements a per-user sliding-window rate limiter.
type rateLimiter struct {
	mu       sync.Mutex
	windows  map[string][]time.Time
	cfg      RateLimitCfg
	stopOnce sync.Once
	stopCh   chan struct{}
}

func newRateLimiter(cfg RateLimitCfg) *rateLimiter {
	rl := &rateLimiter{
		windows: make(map[string][]time.Time),
		cfg:     cfg,
		stopCh:  make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// Allow reports whether userID is within the rate limit.
func (rl *rateLimiter) Allow(userID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.cfg.Window)
	ts := rl.windows[userID]
	kept := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= rl.cfg.MaxMessages {
		rl.windows[userID] = kept
		return false
	}
	rl.windows[userID] = append(kept, now)
	return true
}

func (rl *rateLimiter) cleanup() {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-rl.stopCh:
			return
		case <-tick.C:
			rl.mu.Lock()
			now := time.Now()
			for id, ts := range rl.windows {
				kept := ts[:0]
				for _, t := range ts {
					if t.After(now.Add(-rl.cfg.Window)) {
						kept = append(kept, t)
					}
				}
				if len(kept) == 0 {
					delete(rl.windows, id)
				} else {
					rl.windows[id] = kept
				}
			}
			rl.mu.Unlock()
		}
	}
}

func (rl *rateLimiter) Stop() {
	rl.stopOnce.Do(func() { close(rl.stopCh) })
}

// UserRole defines the permissions for a group of users.
type UserRole struct {
	Name         string
	DisabledCmds map[string]bool
	RateLimitCfg RateLimitCfg
	limiter      *rateLimiter
}

// RoleInput is used to configure a named role.
type RoleInput struct {
	Name             string
	UserIDs          []string
	DisabledCommands []string
	RateLimit        RateLimitCfg
}

// UserRoleManager maps user IDs to roles and enforces limits.
type UserRoleManager struct {
	mu          sync.RWMutex
	roles       map[string]*UserRole // role name -> role
	userToRole  map[string]string    // userID -> role name
	defaultRole *UserRole
}

// NewUserRoleManager creates a new, empty manager.
func NewUserRoleManager() *UserRoleManager {
	return &UserRoleManager{
		roles:      make(map[string]*UserRole),
		userToRole: make(map[string]string),
	}
}

// Configure sets up the role definitions and default role.
func (m *UserRoleManager) Configure(defaultRoleInput RoleInput, inputs []RoleInput) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// stop any existing limiters
	for _, r := range m.roles {
		if r.limiter != nil {
			r.limiter.Stop()
		}
	}
	if m.defaultRole != nil && m.defaultRole.limiter != nil {
		m.defaultRole.limiter.Stop()
	}

	m.roles = make(map[string]*UserRole)
	m.userToRole = make(map[string]string)

	m.defaultRole = buildRole(defaultRoleInput)

	for _, inp := range inputs {
		role := buildRole(inp)
		m.roles[inp.Name] = role
		for _, uid := range inp.UserIDs {
			m.userToRole[uid] = inp.Name
		}
	}
}

func buildRole(inp RoleInput) *UserRole {
	disabled := make(map[string]bool, len(inp.DisabledCommands))
	for _, cmd := range inp.DisabledCommands {
		disabled[strings.ToLower(cmd)] = true
	}
	r := &UserRole{
		Name:         inp.Name,
		DisabledCmds: disabled,
		RateLimitCfg: inp.RateLimit,
	}
	if inp.RateLimit.MaxMessages > 0 && inp.RateLimit.Window > 0 {
		r.limiter = newRateLimiter(inp.RateLimit)
	}
	return r
}

// ResolveRole returns the role for a given userID (falls back to default).
func (m *UserRoleManager) ResolveRole(userID string) *UserRole {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if name, ok := m.userToRole[userID]; ok {
		if r, ok := m.roles[name]; ok {
			return r
		}
	}
	return m.defaultRole
}

// AllowRate checks if a user is within their rate limit.
// Returns (allowed bool, hasLimit bool).
func (m *UserRoleManager) AllowRate(userID string) (bool, bool) {
	role := m.ResolveRole(userID)
	if role == nil || role.limiter == nil {
		return true, false
	}
	return role.limiter.Allow(userID), true
}

// Stop releases all background goroutines.
func (m *UserRoleManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.roles {
		if r.limiter != nil {
			r.limiter.Stop()
		}
	}
	if m.defaultRole != nil && m.defaultRole.limiter != nil {
		m.defaultRole.limiter.Stop()
	}
}

// pipe is the agent.Pipe implementation.
type pipe struct {
	pctx agent.PipeContext
	mgr  *UserRoleManager
}

// NewPipe creates the userroles pipe. Register it with agent.RegisterPipe.
func NewPipe(pctx agent.PipeContext, mgr *UserRoleManager) agent.Pipe {
	return &pipe{pctx: pctx, mgr: mgr}
}

const disabledCmdsKey = "disabled_cmds"

// Handle enforces rate limits. Returns true (intercepts) when the user is
// rate-limited so the message is dropped.
func (p *pipe) Handle(ctx context.Context, d agent.Dialog, msg *agent.Message) bool {
	userID := msg.UserID
	if userID == "" {
		return false
	}

	role := p.mgr.ResolveRole(userID)

	// Store disabled commands in session metadata for downstream pipes/skills.
	if role != nil && len(role.DisabledCmds) > 0 {
		sess := p.pctx.Sessions.GetOrCreateActive(msg.SessionKey)
		var cmds []string
		for cmd := range role.DisabledCmds {
			cmds = append(cmds, cmd)
		}
		sess.SetMetadata(disabledCmdsKey, strings.Join(cmds, ","))
	}

	// Check rate limit.
	allowed, hasLimit := p.mgr.AllowRate(userID)
	if hasLimit && !allowed {
		_ = d.Reply(ctx, msg.ReplyCtx, "⏳ Rate limit reached. Please wait before sending another message.")
		return true // intercept: drop the message
	}
	return false
}
