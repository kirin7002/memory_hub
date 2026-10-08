package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"memoryhub/internal/auth"
	"memoryhub/internal/hub"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	Hub   *hub.Service
	Key   []byte
	Demo  *DemoConfig
	turns *turnCache
}

func New(s *hub.Service, key []byte) http.Handler {
	return NewWithDemo(s, key, nil)
}

func NewWithDemo(s *hub.Service, key []byte, demo *DemoConfig) http.Handler {
	server := &Server{Hub: s, Key: key, Demo: demo, turns: newTurnCache()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]string{"status": "alive"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Pool.Ping(r.Context()); err != nil {
			reply(w, 503, map[string]string{"status": "database_unavailable"})
			return
		}
		var ready bool
		if err := s.Pool.QueryRow(r.Context(), `SELECT to_regclass('input_processing_revisions') IS NOT NULL`).Scan(&ready); err != nil || !ready {
			reply(w, 503, map[string]string{"status": "migration_required"})
			return
		}
		reply(w, 200, map[string]string{"status": "ready"})
	})
	routes := []struct {
		path string
		fn   func(http.ResponseWriter, *http.Request, auth.Principal)
	}{
		{"POST /v1/contexts", server.createContext}, {"POST /v1/conversations", server.createConversation},
		{"GET /v1/conversations", server.listConversations}, {"GET /v1/contexts/active", server.activeContexts},
		{"GET /v1/contexts/{id}", server.getContext}, {"POST /v1/contexts/{id}/commands", server.command},
		{"POST /v1/conversations/{id}/messages", server.message}, {"GET /v1/conversations/{id}/events", server.events},
		{"GET /v1/conversations/{id}/timeline", server.timeline},
		{"POST /v1/contexts/{id}/assemble", server.assemble},
		{"POST /v1/device-agent/contexts/{id}/runs/claim", server.claim}, {"POST /v1/device-agent/runs/{id}/renew", server.renew},
		{"POST /v1/device-agent/runs/{id}/interpretations", server.interpret}, {"POST /v1/device-agent/runs/{id}/results", server.result},
		{"POST /v1/device-agent/runs/{id}/fail", server.failRun}, {"GET /v1/runs/{id}", server.getRun}, {"POST /v1/runs/{id}/cancel", server.cancelRun},
		{"POST /v1/device-agent/executions", server.importExecution}, {"POST /v1/device-agent/executions/{id}/reconcile", server.reconcile},
		{"POST /v1/sync/snapshot", server.snapshot}, {"GET /v1/sync/changes", server.changes}, {"POST /v1/sync/ack", server.ack},
		{"GET /v1/sync/notifications", server.sse},
	}
	for _, route := range routes {
		mux.HandleFunc(route.path, server.authenticated(route.fn))
	}
	if demo != nil {
		server.mountDemo(mux)
	}
	return mux
}
func (s *Server) authenticated(next func(http.ResponseWriter, *http.Request, auth.Principal)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			reply(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		p, err := auth.Verify(s.Key, strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			reply(w, 401, map[string]string{"error": "unauthorized"})
			return
		}
		if err = s.Hub.Authorize(r.Context(), p); err != nil {
			writeError(w, err)
			return
		}
		next(w, r, p)
	}
}
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, err error) {
	var e *hub.Error
	if errors.As(err, &e) {
		reply(w, e.Status, map[string]string{"error": e.Code})
		return
	}
	slog.Error("request failed", "error", err)
	reply(w, 500, map[string]string{"error": "internal_error"})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			reply(w, 413, map[string]string{"error": "payload_too_large"})
		} else {
			reply(w, 400, map[string]string{"error": "invalid_json"})
		}
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		reply(w, 400, map[string]string{"error": "invalid_json"})
		return false
	}
	return true
}
func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	reply(w, 200, v)
}
func (s *Server) createContext(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v hub.CreateContext
	if !decode(w, r, &v) {
		return
	}
	out, err := s.Hub.CreateContext(r.Context(), p, r.Header.Get("Idempotency-Key"), v)
	respond(w, out, err)
}
func (s *Server) listConversations(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	out, err := s.Hub.ListConversations(r.Context(), p, r.URL.Query().Get("after_id"))
	respond(w, out, err)
}
func (s *Server) activeContexts(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	out, err := s.Hub.ActiveContexts(r.Context(), p, r.URL.Query().Get("after_id"))
	respond(w, out, err)
}
func (s *Server) getRun(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	out, err := s.Hub.GetRun(r.Context(), p, r.PathValue("id"))
	respond(w, out, err)
}
func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v hub.RunRequest
	if !decode(w, r, &v) {
		return
	}
	out, err := s.Hub.CancelRun(r.Context(), p, r.PathValue("id"), v)
	respond(w, out, err)
}
func (s *Server) failRun(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v hub.RunRequest
	if !decode(w, r, &v) {
		return
	}
	out, err := s.Hub.FailRun(r.Context(), p, r.PathValue("id"), v)
	respond(w, out, err)
}
func (s *Server) createConversation(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v hub.CreateConversation
	if !decode(w, r, &v) {
		return
	}
	out, err := s.Hub.CreateConversation(r.Context(), p, r.Header.Get("Idempotency-Key"), v)
	respond(w, out, err)
}
func (s *Server) getContext(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	out, err := s.Hub.GetContext(r.Context(), p, r.PathValue("id"))
	respond(w, out, err)
}
func (s *Server) command(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v hub.Command
	if !decode(w, r, &v) {
		return
	}
	out, err := s.Hub.Command(r.Context(), p, r.PathValue("id"), v)
	respond(w, out, err)
}
func (s *Server) message(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v hub.Message
	if !decode(w, r, &v) {
		return
	}
	if h := r.Header.Get("Idempotency-Key"); h != "" && h != v.ID {
		reply(w, 400, map[string]string{"error": "id_mismatch"})
		return
	}
	out, err := s.Hub.Message(r.Context(), p, r.PathValue("id"), v)
	respond(w, out, err)
}
func (s *Server) events(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	after, err := hub.ParseSeq(r.URL.Query().Get("after_conv_seq"))
	if err != nil {
		writeError(w, err)
		return
	}
	out, err := s.Hub.Events(r.Context(), p, r.PathValue("id"), after)
	respond(w, out, err)
}
func (s *Server) assemble(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v hub.AssembleRequest
	if !decode(w, r, &v) {
		return
	}
	out, err := s.Hub.Assemble(r.Context(), p, r.PathValue("id"), v)
	respond(w, out, err)
}

func (s *Server) timeline(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	out, err := s.Hub.Timeline(r.Context(), p, r.PathValue("id"), r.URL.Query().Get("cursor"))
	respond(w, out, err)
}
func (s *Server) claim(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v hub.RunRequest
	if !decode(w, r, &v) {
		return
	}
	out, err := s.Hub.Claim(r.Context(), p, r.PathValue("id"), v)
	respond(w, out, err)
}
func (s *Server) renew(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v hub.RunRequest
	if !decode(w, r, &v) {
		return
	}
	out, err := s.Hub.Renew(r.Context(), p, r.PathValue("id"), v)
	respond(w, out, err)
}
func (s *Server) interpret(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v hub.Interpretation
	if !decode(w, r, &v) {
		return
	}
	out, err := s.Hub.Interpret(r.Context(), p, r.PathValue("id"), v)
	respond(w, out, err)
}
func (s *Server) result(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v hub.Result
	if !decode(w, r, &v) {
		return
	}
	out, err := s.Hub.Result(r.Context(), p, r.PathValue("id"), v)
	respond(w, out, err)
}
func (s *Server) importExecution(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v hub.Execution
	if !decode(w, r, &v) {
		return
	}
	out, err := s.Hub.Import(r.Context(), p, v)
	respond(w, out, err)
}
func (s *Server) reconcile(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v hub.RunRequest
	if !decode(w, r, &v) {
		return
	}
	out, err := s.Hub.Reconcile(r.Context(), p, r.PathValue("id"), v)
	respond(w, out, err)
}
func (s *Server) snapshot(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	out, err := s.Hub.Snapshot(r.Context(), p)
	respond(w, out, err)
}
func (s *Server) changes(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	out, err := s.Hub.Changes(r.Context(), p, r.URL.Query().Get("cursor"))
	respond(w, out, err)
}
func (s *Server) ack(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	var v struct {
		Cursor string `json:"cursor"`
	}
	if !decode(w, r, &v) {
		return
	}
	err := s.Hub.Ack(r.Context(), p, v.Cursor)
	respond(w, map[string]bool{"acknowledged": true}, err)
}
func (s *Server) sse(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if _, ok := w.(http.Flusher); !ok {
		writeError(w, errors.New("stream unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var last hub.Seq = -1
	var initialPolicy hub.Seq
	heart := 0
	for {
		if err := s.Hub.Authorize(r.Context(), p); err != nil {
			return
		}
		if p.Expires <= time.Now().Unix() {
			return
		}
		var seq, policy hub.Seq
		if err := s.Hub.Pool.QueryRow(r.Context(), `SELECT committed_seq,policy_epoch FROM user_stream_heads WHERE tenant_id=$1 AND user_id=$2`, p.Tenant, p.User).Scan(&seq, &policy); err != nil {
			return
		}
		controller := http.NewResponseController(w)
		controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if last == -1 {
			initialPolicy = policy
		}
		if policy != initialPolicy {
			fmt.Fprint(w, "event: reset\ndata: {}\n\n")
			controller.Flush()
			return
		}
		if seq != last {
			b, _ := json.Marshal(map[string]hub.Seq{"watermark": seq, "policy_epoch": policy})
			if _, err := fmt.Fprintf(w, "event: changes_available\ndata: %s\n\n", b); err != nil {
				return
			}
			last = seq
			heart = 0
		} else {
			heart++
			if heart >= 15 {
				if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
					return
				}
				heart = 0
			}
		}
		if err := controller.Flush(); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
