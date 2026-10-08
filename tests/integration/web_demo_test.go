package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"memoryhub/internal/auth"
	"memoryhub/internal/httpapi"
	"memoryhub/internal/hub"
	"memoryhub/internal/llm"
)

// TestWebProxyStoresHistoryAndDeduplicates 验证页面调用模型代理、跨端历史和请求去重。
// 模型用本地 HTTP 替身；浏览器响应与静态页面不能包含服务端模型 Key。
func TestWebProxyStoresHistoryAndDeduplicates(t *testing.T) {
	s := setup(t)
	var calls atomic.Int32
	prompts := make(chan string, 2)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer server-only-model-key" {
			t.Error("proxy did not use server credential")
		}
		var body struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls.Add(1)
		prompts <- body.Messages[1].Content
		modelResponse(w, "已记住绿色车票。")
	}))
	defer provider.Close()
	model, err := llm.New(llm.Config{BaseURL: provider.URL, APIKey: "server-only-model-key", Model: "test-qwen", Timeout: time.Second, MaxTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	demo := &httpapi.DemoConfig{Model: model, ModelName: "test-qwen"}
	api := httptest.NewServer(httpapi.NewWithDemo(s, key, demo))
	defer api.Close()
	demo.HubURL = api.URL
	// 页面资产同源提供，内容不注入配置中的凭证。
	for _, path := range []string{"/", "/assets/app.js", "/assets/style.css"} {
		resp, err := api.Client().Get(api.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || strings.Contains(string(raw), "server-only-model-key") {
			t.Fatal("invalid or credential-bearing static asset")
		}
	}
	bootstrap := func(device string) string {
		encoded, _ := json.Marshal(map[string]string{"device_id": device})
		resp, err := api.Client().Post(api.URL+"/v1/demo/session", "application/json", bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&result)
		if resp.StatusCode != 200 || result["model_ready"] != true {
			t.Fatalf("bootstrap failed: %v", result)
		}
		token := result["token"].(string)
		p, err := auth.Verify(key, token)
		if err != nil || p.Device != device || p.Agent != "web-demo" {
			t.Fatal("invalid browser identity")
		}
		return token
	}
	phoneToken, carToken := bootstrap("phone"), bootstrap("car")
	conv := conversation(t, s)
	call := func(token string, body map[string]string, want int) map[string]any {
		encoded, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", api.URL+"/v1/demo/chat", bytes.NewReader(encoded))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", api.URL)
		resp, err := api.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want || bytes.Contains(raw, []byte("server-only-model-key")) {
			t.Fatalf("proxy status %d expected %d: %s", resp.StatusCode, want, raw)
		}
		var result map[string]any
		_ = json.Unmarshal(raw, &result)
		return result
	}
	// 使用聊天类型，避免本用例引入额外字段提取请求。
	created, err := s.CreateConversation(ctx, principal("phone"), "web-chat-create", hub.CreateConversation{ID: "web-chat", TaskType: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	conv = created.Value.(hub.Conversation)
	first := map[string]string{"request_id": "web-phone-1", "conversation_id": conv.ID, "text": "记住我的标记是绿色车票。"}
	call(phoneToken, first, 200)
	<-prompts
	call(phoneToken, first, 200)
	first["text"] = "修改重试正文"
	call(phoneToken, first, 409)
	call(carToken, map[string]string{"request_id": "web-car-1", "conversation_id": conv.ID, "text": "刚才的标记是什么？"}, 200)
	if !strings.Contains(<-prompts, "绿色车票") {
		t.Fatal("phone history not supplied to car model")
	}
	if calls.Load() != 2 {
		t.Fatalf("duplicate request called paid model: %d", calls.Load())
	}
	history, err := s.Events(ctx, principal("car"), conv.ID, 0)
	if err != nil || len(history["events"].([]hub.Event)) != 4 {
		t.Fatalf("missing persisted turns: %v", err)
	}
}

// TestWebDemoRequiresOptInAndRejectsCrossOrigin 验证默认不挂载演示接口以及开发身份入口校验。
func TestWebDemoRequiresOptInAndRejectsCrossOrigin(t *testing.T) {
	s := setup(t)
	plain := httptest.NewServer(httpapi.New(s, key))
	defer plain.Close()
	resp, err := plain.Client().Get(plain.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("demo unexpectedly enabled by default")
	}
	demo := &httpapi.DemoConfig{}
	api := httptest.NewServer(httpapi.NewWithDemo(s, key, demo))
	defer api.Close()
	demo.HubURL = api.URL
	req, _ := http.NewRequest("POST", api.URL+"/v1/demo/session", strings.NewReader(`{"device_id":"phone"}`))
	req.Header.Set("Origin", "https://unrelated.example")
	resp, err = api.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross-site browser obtained demo identity")
	}
	resp, err = api.Client().Post(api.URL+"/v1/demo/session", "application/json", strings.NewReader(`{"device_id":"unknown"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 422 {
		t.Fatal("unknown device accepted")
	}
	resp, err = api.Client().Post(api.URL+"/v1/demo/chat", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("unauthenticated model proxy accepted")
	}
	result := httpCall(t, api, "phone", "POST", "/v1/demo/chat", map[string]string{"request_id": "no-model", "conversation_id": "any", "text": "你好"}, 503, nil)
	if result["error"] != "model_not_configured" {
		t.Fatal("missing model not reported")
	}
}

// TestIndependentClientAccountIsolationAndCatchUp 验证 POST /v1/demo/session 的多账户身份、
// 同账户不同端的历史/Context 同步，以及跨账户访问和游标复用被拒绝。
// 模型仍使用本地 HTTP 替身，不产生付费调用。
func TestIndependentClientAccountIsolationAndCatchUp(t *testing.T) {
	s := setup(t)
	var calls atomic.Int32
	prompts := make(chan string, 4)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls.Add(1)
		prompts <- body.Messages[1].Content
		modelResponse(w, "客户端回复已保存。")
	}))
	defer provider.Close()
	model, err := llm.New(llm.Config{BaseURL: provider.URL, APIKey: "client-test-only", Model: "test-model", Timeout: time.Second, MaxTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	demo := &httpapi.DemoConfig{Model: model, ModelName: "test-model"}
	api := httptest.NewServer(httpapi.NewWithDemo(s, key, demo))
	defer api.Close()
	demo.HubURL = api.URL
	call := func(token, method, path string, body any, want int) map[string]any {
		t.Helper()
		encoded, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, api.URL+path, bytes.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", api.URL)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if path == "/v1/conversations" && method == "POST" {
			req.Header.Set("Idempotency-Key", hub.ID("create"))
		}
		resp, err := api.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s status %d want %d: %s", method, path, resp.StatusCode, want, raw)
		}
		var result map[string]any
		_ = json.Unmarshal(raw, &result)
		return result
	}
	bootstrap := func(user, device, client string) string {
		result := call("", "POST", "/v1/demo/session", map[string]string{"user_id": user, "device_id": device, "client_id": client}, 200)
		token := result["token"].(string)
		p, err := auth.Verify(key, token)
		if err != nil || p.User != user || p.Device != device || p.Agent != "web-"+client || result["user_id"] != user {
			t.Fatal("client identity not bound to requested development account")
		}
		return token
	}
	alicePhone := bootstrap("alice", "phone", "page-phone")
	aliceCar := bootstrap("alice", "car", "page-car")
	aliceCloud := bootstrap("alice", "web", "page-cloud")
	bobPhone := bootstrap("bob", "phone", "page-bob")
	_ = bootstrap("bob", "web", "page-web")
	create := func(token string) map[string]any {
		return call(token, "POST", "/v1/conversations", map[string]string{"conversation_id": "same-id", "title": "客户端会话", "task_type": "chat"}, 200)["value"].(map[string]any)
	}
	aliceConv, bobConv := create(alicePhone), create(bobPhone)
	if aliceConv["context_id"] == bobConv["context_id"] {
		t.Fatal("accounts share context identity")
	}
	// 相同会话 ID 和 request_id 也必须按账户隔离，不误用另一账户的代理回执。
	call(alicePhone, "POST", "/v1/demo/chat", map[string]string{"request_id": "same-turn", "conversation_id": "same-id", "text": "alice-only-marker"}, 200)
	<-prompts
	call(bobPhone, "POST", "/v1/demo/chat", map[string]string{"request_id": "same-turn", "conversation_id": "same-id", "text": "bob-only-marker"}, 200)
	if prompt := <-prompts; strings.Contains(prompt, "alice-only-marker") || !strings.Contains(prompt, "bob-only-marker") {
		t.Fatal("model prompt crossed account scope")
	}
	call(bobPhone, "GET", "/v1/contexts/"+aliceConv["context_id"].(string), nil, 404)
	call(aliceCar, "GET", "/v1/contexts/"+aliceConv["context_id"].(string), nil, 200)
	carSnapshot := call(aliceCar, "POST", "/v1/sync/snapshot", nil, 200)
	cursor := carSnapshot["cursor"].(string)
	if len(carSnapshot["events"].([]any)) != 2 {
		t.Fatal("same account car missing phone history")
	}
	call(bobPhone, "GET", "/v1/sync/changes?cursor="+cursor, nil, 400)
	// 模拟端侧错过更新后，原设备游标仍可补齐；每页不会借用另一端游标。
	call(alicePhone, "POST", "/v1/demo/chat", map[string]string{"request_id": "next-turn", "conversation_id": "same-id", "text": "继续读取此前标记"}, 200)
	if prompt := <-prompts; !strings.Contains(prompt, "alice-only-marker") || strings.Contains(prompt, "bob-only-marker") {
		t.Fatal("reconnected account history incorrect")
	}
	change := call(aliceCar, "GET", "/v1/sync/changes?cursor="+cursor, nil, 200)
	if len(change["changes"].([]any)) == 0 {
		t.Fatal("missed changes not returned to original device")
	}
	// 第三个独立入口使用 web 同步身份，由同一 Go 代理读取前两端历史并生成回复。
	call(aliceCloud, "POST", "/v1/demo/chat", map[string]string{"request_id": "cloud-turn", "conversation_id": "same-id", "text": "云端继续此会话"}, 200)
	if prompt := <-prompts; !strings.Contains(prompt, "alice-only-marker") || strings.Contains(prompt, "bob-only-marker") {
		t.Fatal("third client did not use the same account history")
	}
	thirdSnapshot := call(aliceCloud, "POST", "/v1/sync/snapshot", nil, 200)
	if len(thirdSnapshot["events"].([]any)) != 6 {
		t.Fatal("third client missing persisted turns")
	}
	call(alicePhone, "POST", "/v1/conversations", map[string]string{"conversation_id": "alice-only", "task_type": "chat"}, 200)
	call(bobPhone, "GET", "/v1/conversations/alice-only/events", nil, 404)
	call(bobPhone, "POST", "/v1/demo/chat", map[string]string{"request_id": "denied", "conversation_id": "alice-only", "text": "不能读取"}, 404)
	if calls.Load() != 4 {
		t.Fatal("unauthorized conversation invoked model")
	}
	resp, err := api.Client().Get(api.URL + "/client")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Contains(raw, []byte(`data-client-mode="single"`)) {
		t.Fatal("independent client page missing")
	}
	for path, role := range map[string]string{"/phone": "phone", "/car": "car", "/cloud": "cloud"} {
		resp, err := api.Client().Get(api.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !bytes.Contains(raw, []byte(`data-client-role="`+role+`"`)) {
			t.Fatalf("fixed client role missing: %s", path)
		}
	}
}

// TestIndependentClientValidationPreservesRevocation 验证非法账户/客户端被拒绝，
// 连接自动准备开发身份不能重新激活 revoked 设备。
func TestIndependentClientValidationPreservesRevocation(t *testing.T) {
	s := setup(t)
	demo := &httpapi.DemoConfig{}
	api := httptest.NewServer(httpapi.NewWithDemo(s, key, demo))
	defer api.Close()
	demo.HubURL = api.URL
	bootstrap := func(body string, want int) {
		t.Helper()
		resp, err := api.Client().Post(api.URL+"/v1/demo/session", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("bootstrap status %d want %d", resp.StatusCode, want)
		}
	}
	bootstrap(`{"user_id":"../alice","device_id":"phone"}`, 422)
	bootstrap(`{"user_id":"alice","device_id":"phone","client_id":"invalid/client"}`, 422)
	bootstrap(`{"user_id":"alice","device_id":"phone","client_id":"page-one"}`, 200)
	if _, err := s.Pool.Exec(ctx, `UPDATE devices SET status='revoked' WHERE tenant_id='demo' AND user_id='alice' AND device_id='phone'`); err != nil {
		t.Fatal(err)
	}
	bootstrap(`{"user_id":"alice","device_id":"phone","client_id":"page-two"}`, 404)
	var status string
	if err := s.Pool.QueryRow(ctx, `SELECT status FROM devices WHERE tenant_id='demo' AND user_id='alice' AND device_id='phone'`).Scan(&status); err != nil || status != "revoked" {
		t.Fatal("revoked device was reactivated")
	}
}
