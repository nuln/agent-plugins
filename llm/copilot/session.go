package copilot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/nuln/agent-core"
)

// copilotSession manages a conversation with the Copilot CLI.
// Each Send() launches a new `copilot -p ... --output-format json --stream on` process,
// optionally using --resume for conversation continuity.
type copilotSession struct {
	workDir  string
	model    string
	mode     string
	extraEnv []string
	events   chan agent.Event
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	alive    atomic.Bool

	sessionID atomic.Value // stores string — Copilot session ID
}

func newCopilotSession(ctx context.Context, workDir, model, mode, resumeID string, extraEnv []string) *copilotSession {
	sessionCtx, cancel := context.WithCancel(ctx)

	cs := &copilotSession{
		workDir:  workDir,
		model:    model,
		mode:     mode,
		extraEnv: extraEnv,
		events:   make(chan agent.Event, 64),
		ctx:      sessionCtx,
		cancel:   cancel,
	}
	cs.alive.Store(true)

	if resumeID != "" {
		cs.sessionID.Store(resumeID)
	}

	return cs
}

func (cs *copilotSession) Send(prompt string, _ []agent.ImageAttachment, _ []agent.FileAttachment) (err error) {
	if !cs.alive.Load() {
		return fmt.Errorf("session is closed")
	}

	sid := cs.currentSessionID()
	isResume := sid != ""

	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--stream", "on",
		"--silent",
	}

	// Permission mode flags
	switch cs.mode {
	case "autopilot":
		args = append(args, "--autopilot", "--allow-all-tools")
	case "yolo":
		args = append(args, "--yolo")
	default:
		// Default still needs --allow-all-tools for non-interactive mode
		args = append(args, "--allow-all-tools")
	}

	if isResume {
		args = append(args, "--resume="+sid)
	}
	if cs.model != "" {
		args = append(args, "--model", cs.model)
	}

	ctx, cancel := context.WithCancel(cs.ctx)

	// Ensure cancel is called on early return errors
	started := false
	defer func() {
		if !started {
			cancel()
		}
	}()

	slog.Debug("copilotSession: launching", "resume", isResume, "args", redactArgs(args))
	cmd := exec.CommandContext(ctx, "copilot", args...)
	cmd.WaitDelay = 1 * time.Second
	cmd.Dir = cs.workDir
	env := os.Environ()
	if len(cs.extraEnv) > 0 {
		env = mergeEnv(env, cs.extraEnv)
	}
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("copilotSession: stdout pipe: %w", err)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("copilotSession: start: %w", err)
	}

	started = true
	cs.wg.Add(1)
	go func() {
		defer cancel()
		cs.readLoop(ctx, cmd, stdout, &stderrBuf)
	}()

	return nil
}

func (cs *copilotSession) readLoop(ctx context.Context, cmd *exec.Cmd, stdout io.ReadCloser, stderrBuf *bytes.Buffer) {
	defer cs.wg.Done()
	defer func() {
		if err := cmd.Wait(); err != nil {
			stderrMsg := strings.TrimSpace(stderrBuf.String())
			if stderrMsg != "" {
				slog.Error("copilotSession: process failed", "error", err, "stderr", stderrMsg)
				evt := agent.Event{Type: agent.EventError, Error: fmt.Errorf("%s", stderrMsg)}
				select {
				case cs.events <- evt:
				case <-cs.ctx.Done():
					return
				}
			}
		}
	}()

	// Unblock scanner if context is canceled
	go func() {
		<-ctx.Done()
		_ = stdout.Close()
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		slog.Debug("copilotSession: raw", "line", truncate(line, 500))

		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			slog.Debug("copilotSession: non-JSON line", "line", line)
			continue
		}

		cs.handleEvent(raw)
	}

	if err := scanner.Err(); err != nil {
		slog.Error("copilotSession: scanner error", "error", err)
		evt := agent.Event{Type: agent.EventError, Error: fmt.Errorf("read stdout: %w", err)}
		select {
		case cs.events <- evt:
		case <-cs.ctx.Done():
			return
		}
	}
}

// Copilot CLI JSONL event types:
//
//	session.tools_updated      — session initialization, model info
//	user.message               — user prompt echo
//	assistant.turn_start       — turn begins
//	assistant.reasoning_delta  — thinking/reasoning stream (ephemeral)
//	assistant.reasoning        — complete reasoning block (ephemeral)
//	assistant.message_delta    — incremental text content (ephemeral)
//	assistant.message          — complete message with optional toolRequests
//	tool.execution_start       — tool execution begins
//	tool.execution_complete    — tool execution result
//	assistant.turn_end         — turn ends
//	result                     — final result with usage stats and sessionId
func (cs *copilotSession) handleEvent(raw map[string]any) {
	eventType, _ := raw["type"].(string)

	switch eventType {
	case "assistant.message_delta":
		cs.handleMessageDelta(raw)
	case "assistant.reasoning_delta":
		cs.handleReasoningDelta(raw)
	case "assistant.message":
		cs.handleMessage(raw)
	case "tool.execution_start":
		cs.handleToolStart(raw)
	case "tool.execution_complete":
		cs.handleToolComplete(raw)
	case "result":
		cs.handleResult(raw)
	case "session.tools_updated", "session.mcp_servers_loaded",
		"user.message", "assistant.turn_start", "assistant.turn_end",
		"assistant.reasoning", "session.background_tasks_changed":
		// Known events we can safely skip
		slog.Debug("copilotSession: skipping event", "type", eventType)
	default:
		slog.Debug("copilotSession: unhandled event", "type", eventType)
	}
}

func (cs *copilotSession) handleMessageDelta(raw map[string]any) {
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		return
	}
	content, _ := data["deltaContent"].(string)
	if content == "" {
		return
	}

	evt := agent.Event{Type: agent.EventText, Content: content}
	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
	}
}

func (cs *copilotSession) handleReasoningDelta(raw map[string]any) {
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		return
	}
	content, _ := data["deltaContent"].(string)
	if content == "" {
		return
	}

	evt := agent.Event{Type: agent.EventThinking, Content: content}
	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
	}
}

func (cs *copilotSession) handleMessage(raw map[string]any) {
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		return
	}

	// Extract tool requests from the message
	toolRequests, _ := data["toolRequests"].([]any)
	for _, tr := range toolRequests {
		trMap, ok := tr.(map[string]any)
		if !ok {
			continue
		}
		toolName, _ := trMap["name"].(string)
		args, _ := trMap["arguments"].(map[string]any)

		input := formatToolInput(toolName, args)

		evt := agent.Event{
			Type:         agent.EventToolUse,
			ToolName:     toolName,
			ToolInput:    input,
			ToolInputRaw: args,
		}
		select {
		case cs.events <- evt:
		case <-cs.ctx.Done():
			return
		}
	}
}

func (cs *copilotSession) handleToolStart(raw map[string]any) {
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		return
	}
	toolName, _ := data["toolName"].(string)
	args, _ := data["arguments"].(map[string]any)

	slog.Debug("copilotSession: tool started", "tool", toolName)

	// If the message handler already emitted EventToolUse, we skip here.
	// To avoid duplicates, we only emit from handleMessage which has the full context.
	_ = toolName
	_ = args
}

func (cs *copilotSession) handleToolComplete(raw map[string]any) {
	data, _ := raw["data"].(map[string]any)
	if data == nil {
		return
	}
	toolName, _ := data["toolName"].(string)
	if toolName == "" {
		toolCallID, _ := data["toolCallId"].(string)
		toolName = toolCallID
	}

	success, _ := data["success"].(bool)
	result, _ := data["result"].(map[string]any)

	var content string
	if result != nil {
		content, _ = result["content"].(string)
		if content == "" {
			content, _ = result["detailedContent"].(string)
		}
	}

	if !success && content == "" {
		content = "Tool execution failed"
	}

	if content != "" {
		evt := agent.Event{
			Type:     agent.EventToolResult,
			ToolName: toolName,
			Content:  truncate(content, 500),
		}
		select {
		case cs.events <- evt:
		case <-cs.ctx.Done():
			return
		}
	}
}

func (cs *copilotSession) handleResult(raw map[string]any) {
	// Extract and store session ID from the result event
	sid, _ := raw["sessionId"].(string)
	if sid != "" {
		cs.sessionID.Store(sid)
	}

	evt := agent.Event{
		Type:      agent.EventResult,
		SessionID: sid,
		Done:      true,
	}

	// Check exit code for errors
	exitCode, _ := raw["exitCode"].(float64)
	if exitCode != 0 {
		evt.Error = fmt.Errorf("copilot exited with code %d", int(exitCode))
	}

	select {
	case cs.events <- evt:
	case <-cs.ctx.Done():
	}
}

func (cs *copilotSession) Events() <-chan agent.Event {
	return cs.events
}

func (cs *copilotSession) currentSessionID() string {
	v, _ := cs.sessionID.Load().(string)
	return v
}

func (cs *copilotSession) Close() error {
	cs.alive.Store(false)
	cs.cancel()
	done := make(chan struct{})
	go func() {
		cs.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		slog.Warn("copilotSession: close timed out, abandoning wg.Wait")
	}
	close(cs.events)
	return nil
}

// formatToolInput extracts a human-readable summary from tool arguments.
func formatToolInput(toolName string, args map[string]any) string {
	if args == nil {
		return ""
	}

	switch toolName {
	case "bash", "shell":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	case "read", "write", "edit", "Read", "Write", "Edit":
		if fp, ok := args["file_path"].(string); ok {
			return fp
		}
		if fp, ok := args["path"].(string); ok {
			return fp
		}
	case "grep", "Grep":
		if p, ok := args["pattern"].(string); ok {
			return p
		}
	case "glob", "Glob":
		if p, ok := args["pattern"].(string); ok {
			return p
		}
		if p, ok := args["glob_pattern"].(string); ok {
			return p
		}
	case "report_intent":
		if intent, ok := args["intent"].(string); ok {
			return intent
		}
	}

	b, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(b)
}

func truncate(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes]) + "..."
}

// redactArgs returns a copy of args with values after sensitive flag names masked.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)

	sensitiveFlags := []string{
		"--api-key", "--token", "--secret", "--password",
	}

	for i := 0; i < len(out); i++ {
		arg := strings.ToLower(out[i])

		// --flag=value format
		for _, f := range sensitiveFlags {
			if strings.HasPrefix(arg, f+"=") {
				out[i] = out[i][:strings.Index(out[i], "=")+1] + "***"
				break
			}
		}

		// --flag value format
		for _, f := range sensitiveFlags {
			if arg == f && i+1 < len(out) {
				out[i+1] = "***"
				i++
				break
			}
		}
	}
	return out
}

// mergeEnv merges two environment variable slices.
func mergeEnv(base, extra []string) []string {
	m := make(map[string]string)
	for _, e := range base {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}
	for _, e := range extra {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		}
	}
	res := make([]string, 0, len(m))
	for k, v := range m {
		res = append(res, k+"="+v)
	}
	return res
}
