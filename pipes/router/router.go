package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPipe("router", 900, func(ctx agent.PipeContext) agent.Pipe {
		return &RouterHook{
			sessions:   ctx.Sessions,
			translator: ctx.Translator,
			getAgents:  ctx.GetAgents,
		}
	})
}

// RouterHook handles llm switching commands.
type RouterHook struct {
	sessions   agent.SessionProvider
	translator agent.Translator
	getAgents  func() []agent.AgentInfo
}

func (r *RouterHook) Handle(ctx context.Context, p agent.Dialog, msg *agent.Message) bool {
	if !strings.HasPrefix(msg.Content, "/llm ") {
		return false
	}

	parts := strings.Fields(msg.Content[7:]) // Skip "/llm "
	if len(parts) == 0 {
		return false
	}

	cmd := parts[0]
	session := r.sessions.GetOrCreateActive(msg.SessionKey)

	switch cmd {
	case "list":
		agents := r.getAgents()
		var sb strings.Builder
		sb.WriteString("Available AI Agents:\n")
		for _, a := range agents {
			sb.WriteString(fmt.Sprintf("- **%s**: %s\n", a.Name, a.Description))
		}
		_ = p.Reply(ctx, msg.ReplyCtx, sb.String())
		return true

	case "use":
		if len(parts) < 2 {
			_ = p.Reply(ctx, msg.ReplyCtx, "Usage: /llm use <name>")
			return true
		}
		target := parts[1]
		agents := r.getAgents()
		found := false
		for _, a := range agents {
			if a.Name == target {
				found = true
				break
			}
		}

		if !found {
			_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Error: LLM '%s' not found.", target))
			return true
		}

		session.SetMetadata("llm", target)
		_ = p.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Switched to llm: **%s**", target))
		return true
	}

	return false
}
