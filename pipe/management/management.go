// Package management provides an HTTP management API pipe for the agent.
//
// Configuration (environment variables):
//
//	MANAGEMENT_PORT   - port to listen on (default: 9820)
//	MANAGEMENT_TOKEN  - bearer token for authentication (optional)
//
// Auto-registration: import _ "github.com/nuln/agent-plugins/pipe/management"
package management

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	agent "github.com/nuln/agent-core"
)

func init() {
	agent.RegisterPluginConfigSpec(agent.PluginConfigSpec{
		PluginName:  "management",
		PluginType:  "pipe",
		Description: "HTTP management API for status and agent info",
		Fields: []agent.ConfigField{
			{EnvVar: "MANAGEMENT_PORT", Key: "port", Description: "HTTP listen port", Default: "9820", Type: agent.ConfigFieldInt, Example: "9820"},
			{EnvVar: "MANAGEMENT_TOKEN", Key: "token", Description: "Bearer token for authentication", Type: agent.ConfigFieldSecret},
			{EnvVar: "MANAGEMENT_CORS_ORIGIN", Key: "cors_origin", Description: "Allowed CORS origins (comma-separated)", Type: agent.ConfigFieldString, Example: "http://localhost:3000"},
		},
	})

	agent.RegisterPipe("management", 30, func(pctx agent.PipeContext) agent.Pipe {
		port := os.Getenv("MANAGEMENT_PORT")
		if port == "" {
			port = "9820"
		}
		token := os.Getenv("MANAGEMENT_TOKEN")
		corsOrigin := os.Getenv("MANAGEMENT_CORS_ORIGIN")
		p := &mgmtPipe{
			pctx:       pctx,
			port:       port,
			token:      token,
			corsOrigin: corsOrigin,
			startTime:  time.Now(),
		}
		go p.start()
		return p
	})
}

type mgmtPipe struct {
	pctx       agent.PipeContext
	port       string
	token      string
	corsOrigin string
	startTime  time.Time
	httpSrv    *http.Server
}

// Handle is a no-op passthrough; management works via its own HTTP server.
func (p *mgmtPipe) Handle(_ context.Context, _ agent.Dialog, _ *agent.Message) bool {
	return false
}

func (p *mgmtPipe) start() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", p.withAuth(p.handleStatus))
	mux.HandleFunc("/api/v1/agents", p.withAuth(p.handleAgents))

	addr := ":" + p.port
	p.httpSrv = &http.Server{Addr: addr, Handler: mux}
	slog.Info("management: listening", "addr", addr)
	if err := p.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("management: server error", "err", err)
	}
}

func (p *mgmtPipe) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if p.corsOrigin == "*" {
			// Explicitly configured open CORS
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else if p.corsOrigin != "" && origin != "" {
			// Check against allowed origins list
			for _, o := range strings.Split(p.corsOrigin, ",") {
				if strings.TrimSpace(o) == origin {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Add("Vary", "Origin")
					break
				}
			}
		}
		// When corsOrigin is empty, no CORS headers are emitted (default-deny).
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if p.token != "" {
			tok := r.Header.Get("Authorization")
			expected := "Bearer " + p.token
			if subtle.ConstantTimeCompare([]byte(tok), []byte(expected)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (p *mgmtPipe) handleStatus(w http.ResponseWriter, _ *http.Request) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	resp := map[string]interface{}{
		"status":     "ok",
		"uptime_sec": int64(time.Since(p.startTime).Seconds()),
		"memory_mb":  fmt.Sprintf("%.2f", float64(ms.Alloc)/1024/1024),
		"goroutines": runtime.NumGoroutine(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (p *mgmtPipe) handleAgents(w http.ResponseWriter, _ *http.Request) {
	var names []string
	if p.pctx.GetAgents != nil {
		for _, d := range p.pctx.GetAgents() {
			names = append(names, d.Name)
		}
	}
	if names == nil {
		names = []string{}
	}
	resp := map[string]interface{}{"agents": names}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
