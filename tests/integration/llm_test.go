package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"memoryhub/internal/deviceagent"
	"memoryhub/internal/httpapi"
	"memoryhub/internal/hub"
	"memoryhub/internal/llm"
)

func modelResponse(w http.ResponseWriter, text string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"role": "assistant", "content": text}}}})
}

// TestLLMChatCrossDeviceAndProviderFailure 通过完整 HTTP 与真实 PostgreSQL 验证多轮接续。
// 付费模型用兼容接口测试服务替代；验证 assemble 历史真正进入模型请求，以及失败释放 Run。
func TestLLMChatCrossDeviceAndProviderFailure(t *testing.T) {
	s := setup(t)
	api := httptest.NewServer(httpapi.New(s, key))
	defer api.Close()
	prompts := make(chan string, 3)
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer test-provider-key" {
			t.Error("model endpoint/auth mismatch")
		}
		var body struct {
			Messages []llm.Message `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 2 {
			t.Error("invalid model messages")
			w.WriteHeader(400)
			return
		}
		prompts <- body.Messages[1].Content
		if calls.Add(1) == 3 {
			w.WriteHeader(429)
			return
		}
		modelResponse(w, "我记得你说过喜欢杭州。")
	}))
	defer provider.Close()
	model, err := llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test-provider-key", Model: "test-qwen", Timeout: time.Second, MaxTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	phone := &deviceagent.Client{BaseURL: api.URL, Key: key, Device: "phone", HTTP: api.Client()}
	conv, err := phone.Conversation(ctx, "", "chat")
	if err != nil {
		t.Fatal(err)
	}
	session := &deviceagent.Session{Hub: phone, Model: model, Conversation: conv, Boot: "boot-phone"}
	if _, err = session.Turn(ctx, "我喜欢杭州，请记住。"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(<-prompts, "我喜欢杭州") {
		t.Fatal("current input not sent to model")
	}
	car := &deviceagent.Client{BaseURL: api.URL, Key: key, Device: "car", HTTP: api.Client()}
	resumed, err := car.Conversation(ctx, conv.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	session = &deviceagent.Session{Hub: car, Model: model, Conversation: resumed, Boot: "boot-car"}
	if _, err = session.Turn(ctx, "我喜欢哪里？"); err != nil {
		t.Fatal(err)
	}
	prompt := <-prompts
	if !strings.Contains(prompt, "我喜欢杭州") || !strings.Contains(prompt, "我喜欢哪里") || !strings.Contains(prompt, "我记得你说过") {
		t.Fatal("cross-device prior turns missing from actual model request")
	}
	answer, err := session.Turn(ctx, "触发厂商限流")
	if err == nil || answer != "" {
		t.Fatal("provider failure incorrectly succeeded")
	}
	<-prompts
	var status string
	if err = s.Pool.QueryRow(ctx, `SELECT status FROM agent_runs WHERE context_id=$1 ORDER BY created_at DESC LIMIT 1`, conv.ContextID).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("failed call did not close Run: %s %v", status, err)
	}
	var history struct {
		Events []hub.Event `json:"events"`
	}
	if err = car.Call(ctx, "GET", "/v1/conversations/"+conv.ID+"/events", nil, nil, &history); err != nil {
		t.Fatal(err)
	}
	assistants := 0
	for _, e := range history.Events {
		if e.Type == "assistant_message" {
			assistants++
		}
	}
	if assistants != 2 {
		t.Fatalf("unexpected persisted model replies: %d", assistants)
	}
}

// TestLLMTripUsesValidatedStateForReply 验证提取候选 → 云端 interpretations → 新 Bundle → 回复。
// 模型不能直接确认字段；这里明确用户表达通过服务器规则确认后，回复模型才看到确认状态。
func TestLLMTripUsesValidatedStateForReply(t *testing.T) {
	s := setup(t)
	api := httptest.NewServer(httpapi.New(s, key))
	defer api.Close()
	var extracted atomic.Bool
	var validated atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []llm.Message     `json:"messages"`
			Format   map[string]string `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) != 2 {
			t.Error("invalid model request")
			w.WriteHeader(400)
			return
		}
		if body.Format["type"] == "json_object" {
			var input struct {
				Pending []struct {
					ID string `json:"event_id"`
				} `json:"pending_inputs"`
			}
			if err := json.Unmarshal([]byte(body.Messages[1].Content), &input); err != nil || len(input.Pending) != 1 {
				t.Error("missing extraction source")
				w.WriteHeader(400)
				return
			}
			extracted.Store(true)
			patch, _ := json.Marshal(map[string]any{"patches": []any{map[string]any{"field": "destination", "value": "上海", "source_event_ids": []string{input.Pending[0].ID}}}})
			modelResponse(w, string(patch))
			return
		}
		var reply struct {
			Confirmed map[string]hub.Slot `json:"confirmed_state"`
		}
		if json.Unmarshal([]byte(body.Messages[1].Content), &reply) == nil && reply.Confirmed["destination"].Value == "上海" && reply.Confirmed["destination"].Status == "confirmed" {
			validated.Store(true)
		}
		modelResponse(w, "已记录上海行程。")
	}))
	defer provider.Close()
	model, err := llm.New(llm.Config{BaseURL: provider.URL, APIKey: "test-key", Model: "test-qwen", Timeout: time.Second, MaxTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	phone := &deviceagent.Client{BaseURL: api.URL, Key: key, Device: "phone", HTTP: api.Client()}
	conv, err := phone.Conversation(ctx, "", "trip_planning")
	if err != nil {
		t.Fatal(err)
	}
	session := &deviceagent.Session{Hub: phone, Model: model, Conversation: conv, Boot: "boot"}
	if _, err = session.Turn(ctx, "目的地是上海"); err != nil {
		t.Fatal(err)
	}
	if !extracted.Load() || !validated.Load() {
		t.Fatal("reply did not use state validated after extraction")
	}
	c, err := s.GetContext(ctx, principal("car"), conv.ContextID)
	if err != nil || c.State.Slots["destination"].Value != "上海" || len(c.State.PendingInputs) != 0 {
		t.Fatalf("unexpected persisted state: %+v %v", c, err)
	}
}
