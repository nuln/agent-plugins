// Package webhook provides an HTTP webhook receiver pipe.
//
// Configuration (environment variables):
//
//	WEBHOOK_PORT   - port to listen on (default: 9111)
//	WEBHOOK_TOKEN  - bearer token for authentication (optional)
//	WEBHOOK_PATH   - HTTP path to listen on (default: /hook)
//
// Auto-registration: import _ "github.com/nuln/agent-plugins/pipe/webhook"
package webhook

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	agent "github.com/nuln/agent-core"
)

// WebhookRequest is the JSON body expected by the webhook endpoint.
type WebhookRequest struct {
	Event      string `json:"event"`
	SessionKey string `json:"session_key"`
	Prompt     string `json:"prompt"`
	Exec       string `json:"exec"`
	WorkDir    string `json:"work_dir"`
	Silent     bool   `json:"silent"`
	Payload    any    `json:"payload"`
}

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "webhook",
		PluginType:  "pipe",
		Description: "HTTP webhook receiver for external event injection",
		Fields: []agent.ConfigField{
			{EnvVar: "WEBHOOK_PORT", Key: "port", Description: "HTTP listen port", Default: "9111", Type: agent.ConfigFieldInt, Example: "9111"},
			{EnvVar: "WEBHOOK_TOKEN", Key: "token", Description: "Bearer token for authentication", Type: agent.ConfigFieldSecret},
			{EnvVar: "WEBHOOK_PATH", Key: "path", Description: "HTTP path to listen on", Default: "/hook", Type: agent.ConfigFieldString, Example: "/hook"},
		},
	})

	agent.RegisterPipe("webhook", 50, func(pctx agent.PipeContext) agent.Pipe {
		port := os.Getenv("WEBHOOK_PORT")
		if port == "" {
			port = "9111"
		}
		path := os.Getenv("WEBHOOK_PATH")
		if path == "" {
			path = "/hook"
		}
		token := os.Getenv("WEBHOOK_TOKEN")
		s := &webhookPipe{
			pctx:  pctx,
			port:  port,
			path:  path,
			token: token,
		}
		go s.start()
		return s
	})
}

type webhookPipe struct {
	pctx  agent.PipeContext
	port  string
	path  string
	token string
}

// Handle is a no-op passthrough; webhook listens on its own HTTP server.
func (s *webhookPipe) Handle(_ context.Context, _ agent.Dialog, _ *agent.Message) bool {
	return false
}

func (s *webhookPipe) start() {
	mux := http.NewServeMux()
	mux.HandleFunc(s.path, s.handleHook)
	addr := ":" + s.port
	slog.Info("webhook: listening", "addr", addr, "path", s.path)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("webhook: server error", "err", err)
	}
}

func (s *webhookPipe) authenticate(r *http.Request) bool {
	if s.token == "" {
		return true
	}
	expected := []byte(s.token)
	// Check Authorization: Bearer <token>
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), expected) == 1
	}
	// Check X-Webhook-Token header
	if t := r.Header.Get("X-Webhook-Token"); t != "" {
		return subtle.ConstantTimeCompare([]byte(t), expected) == 1
	}
	// Check query parameter
	if t := r.URL.Query().Get("token"); t != "" {
		return subtle.ConstantTimeCompare([]byte(t), expected) == 1
	}
	return false
}

func (s *webhookPipe) handleHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authenticate(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req WebhookRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.SessionKey == "" {
		http.Error(w, "session_key is required", http.StatusBadRequest)
		return
	}
	go s.executePrompt(req)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"accepted"}`))
}

func (s *webhookPipe) executePrompt(req WebhookRequest) {
	if s.pctx.Inject == nil {
		slog.Warn("webhook: Inject not available")
		return
	}
	prompt := req.Prompt
	if prompt == "" && req.Event != "" {
		b, _ := json.Marshal(req)
		prompt = "Webhook event: " + string(b)
	}
	if prompt == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	slog.Debug("webhook: injecting", "session_key", req.SessionKey, "event", req.Event)
	s.pctx.Inject(ctx, req.SessionKey, prompt)
}
