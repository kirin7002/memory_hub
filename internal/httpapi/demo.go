package httpapi

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"memoryhub/internal/auth"
	"memoryhub/internal/deviceagent"
	"memoryhub/internal/hub"
	"memoryhub/internal/store"
	"memoryhub/internal/webui"
)

// DemoConfig 仅在显式开启的本地演示模式挂载页面及模型代理。
// HubURL 来自服务配置，绝不从浏览器的 Host 或请求体决定内部访问地址。
type DemoConfig struct {
	HubURL    string
	Model     deviceagent.Model
	ModelName string
}

type turnResult struct {
	status  int
	body    any
	hash    [32]byte
	expires time.Time
	done    bool
}
type turnCache struct {
	mu      sync.Mutex
	entries map[string]*turnResult
}

func newTurnCache() *turnCache { return &turnCache{entries: map[string]*turnResult{}} }

// sameOrigin 拒绝浏览器跨站访问开发身份入口；localhost 与 127.0.0.1 视为本机别名。
func (s *Server) sameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		reply(w, 403, map[string]string{"error": "cross_origin_denied"})
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		got, err := url.Parse(origin)
		want, err2 := url.Parse(s.Demo.HubURL)
		normalize := func(host string) string {
			if host == "localhost" {
				return "127.0.0.1"
			}
			return host
		}
		if err != nil || err2 != nil || got.Scheme != want.Scheme || normalize(got.Hostname()) != normalize(want.Hostname()) || got.Port() != want.Port() {
			reply(w, 403, map[string]string{"error": "cross_origin_denied"})
			return false
		}
	}
	return true
}

func (s *Server) mountDemo(mux *http.ServeMux) {
	mux.Handle("GET /{$}", webui.Handler())
	mux.Handle("GET /client", webui.Handler())
	mux.Handle("GET /phone", webui.Handler())
	mux.Handle("GET /car", webui.Handler())
	mux.Handle("GET /cloud", webui.Handler())
	mux.Handle("GET /assets/", webui.Handler())
	mux.HandleFunc("POST /v1/demo/session", s.demoSession)
	mux.HandleFunc("POST /v1/demo/chat", s.authenticated(s.demoChat))
}

var demoIdentity = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

// demoSession 发放短期开发设备凭证；返回值不含模型 Key 或开发签名密钥。
func (s *Server) demoSession(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(w, r) {
		return
	}
	var body struct {
		Device string `json:"device_id"`
		User   string `json:"user_id,omitempty"`
		Client string `json:"client_id,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Device != "phone" && body.Device != "car" && body.Device != "web" {
		reply(w, 422, map[string]string{"error": "device_invalid"})
		return
	}
	user, agent := "demo-user", "web-demo"
	if body.Client != "" {
		if !demoIdentity.MatchString(body.Client) {
			reply(w, 422, map[string]string{"error": "client_invalid"})
			return
		}
		agent = "web-" + body.Client
	}
	if body.User != "" {
		if !demoIdentity.MatchString(body.User) {
			reply(w, 422, map[string]string{"error": "account_invalid"})
			return
		}
		user = body.User
		// 开发账户连接即准备测试身份；此入口随 WEB_DEMO_ENABLED 关闭。
		if err := store.EnsureDemoAccount(r.Context(), s.Hub.Pool, user); err != nil {
			writeError(w, err)
			return
		}
	}
	p := auth.Principal{Tenant: "demo", User: user, Device: body.Device, Agent: agent, Expires: time.Now().Add(30 * time.Minute).Unix()}
	if err := s.Hub.Authorize(r.Context(), p); err != nil {
		writeError(w, err)
		return
	}
	reply(w, 200, map[string]any{"token": auth.Sign(s.Key, p), "expires_at": p.Expires, "tenant_id": p.Tenant, "user_id": p.User, "device_id": p.Device, "agent_id": p.Agent, "model": s.Demo.ModelName, "model_ready": s.Demo.Model != nil})
}

// demoChat 是前端的模型代理接口：先通过 MemoryHub 保存/组装，再调用服务持有的模型。
// request_id 对同一设备短期去重；不能把模型 Key、模型地址或任意身份放进浏览器请求。
func (s *Server) demoChat(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if !s.sameOrigin(w, r) {
		return
	}
	if s.Demo.Model == nil {
		reply(w, 503, map[string]string{"error": "model_not_configured"})
		return
	}
	var body struct {
		ID           string `json:"request_id"`
		Conversation string `json:"conversation_id"`
		Text         string `json:"text"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ID == "" || len(body.ID) > 128 || body.Conversation == "" || len(body.Conversation) > 128 || strings.TrimSpace(body.Text) == "" || len(body.Text) > 32*1024 {
		reply(w, 422, map[string]string{"error": "invalid_chat_request"})
		return
	}
	cacheKey := p.Tenant + "/" + p.User + "/" + p.Device + "/" + body.ID
	encoded, _ := json.Marshal(body)
	hash := sha256.Sum256(encoded)
	s.turns.mu.Lock()
	for key, entry := range s.turns.entries {
		if entry.done && time.Now().After(entry.expires) {
			delete(s.turns.entries, key)
		}
	}
	if prior, ok := s.turns.entries[cacheKey]; ok {
		status, result := prior.status, prior.body
		if prior.hash != hash {
			status, result = 409, map[string]string{"error": "idempotency_conflict"}
		} else if !prior.done {
			status, result = 409, map[string]string{"error": "request_in_progress"}
		}
		s.turns.mu.Unlock()
		reply(w, status, result)
		return
	}
	if len(s.turns.entries) >= 512 {
		s.turns.mu.Unlock()
		reply(w, 429, map[string]string{"error": "demo_request_limit"})
		return
	}
	entry := &turnResult{hash: hash}
	s.turns.entries[cacheKey] = entry
	s.turns.mu.Unlock()
	finish := func(status int, result any) {
		s.turns.mu.Lock()
		entry.status = status
		entry.body = result
		entry.done = true
		entry.expires = time.Now().Add(10 * time.Minute)
		s.turns.mu.Unlock()
		reply(w, status, result)
	}
	client := &deviceagent.Client{BaseURL: s.Demo.HubURL, Key: s.Key, Device: p.Device, Principal: &p, HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
	conv, err := client.Conversation(r.Context(), body.Conversation, "")
	if err != nil {
		finish(404, map[string]string{"error": "conversation_unavailable"})
		return
	}
	// 复用同一服务里的 Agent 编排，但执行身份仍绑定当前模拟设备；不修改 cloud 目标能力。
	session := &deviceagent.Session{Hub: client, Model: s.Demo.Model, Conversation: conv, Boot: hub.ID("web_boot")}
	answer, err := session.Turn(r.Context(), body.Text)
	if err != nil {
		code, status := "chat_failed", 502
		var business *deviceagent.HTTPError
		if errors.As(err, &business) {
			code, status = business.Code, business.Status
		}
		// 模型客户端已对错误脱敏；不记录用户正文、凭证或厂商响应。
		finish(status, map[string]string{"error": code, "message": err.Error(), "conversation_id": conv.ID})
		return
	}
	finish(200, map[string]string{"request_id": body.ID, "conversation_id": conv.ID, "context_id": conv.ContextID, "reply": answer})
}
