package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "router",
		PluginType:  "pipe",
		Description: "Routes messages to different LLM agents via slash commands",
	})

	agent.RegisterPipe("router", 450, func(ctx agent.PipeContext) agent.Pipe {
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

func (r *RouterHook) Handle(ctx context.Context, d agent.Dialog, msg *agent.Message) bool {
	content := strings.TrimSpace(msg.Content)
	if !strings.HasPrefix(content, "/llm") {
		return false
	}

	parts := strings.Fields(content)
	if len(parts) < 2 {
		return false // Or show help
	}

	cmdOrName := parts[1]
	session := r.sessions.GetOrCreateActive(msg.SessionKey)

	switch cmdOrName {
	case "list":
		agents := r.getAgents()
		var sb strings.Builder
		sb.WriteString("Available AI Agents:\n")
		for _, a := range agents {
			sb.WriteString(fmt.Sprintf("- **%s**: %s\n", a.Name, a.Description))
		}
		_ = d.Reply(ctx, msg.ReplyCtx, sb.String())
		return true

	case "use":
		if len(parts) < 3 {
			_ = d.Reply(ctx, msg.ReplyCtx, "Usage: /llm use <name>")
			return true
		}
		return r.switchToLLM(ctx, d, msg, session, parts[2])

	default:
		// 允许直接使用 /llm <name>
		return r.switchToLLM(ctx, d, msg, session, cmdOrName)
	}
}

func (r *RouterHook) switchToLLM(ctx context.Context, d agent.Dialog, msg *agent.Message, session agent.Session, target string) bool {
	agents := r.getAgents()
	found := false
	for _, a := range agents {
		if a.Name == target {
			found = true
			break
		}
	}

	if !found {
		_ = d.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("Error: LLM '%s' not found.", target))
		return true
	}

	session.SetMetadata("llm", target)
	_ = d.Reply(ctx, msg.ReplyCtx, fmt.Sprintf("✅ 已切换到模型: **%s**", target))
	return true
}
