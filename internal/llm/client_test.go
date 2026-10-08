package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 验证实际请求路径、身份 Header、JSON 模式以及拒绝截断输出，不访问付费模型。
func TestCompatibleCompletionProtocol(t *testing.T) {
	finish := "stop"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/compatible-mode/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" || r.Method != "POST" {
			t.Errorf("invalid model request")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request JSON")
		}
		if body["stream"] != false || body["enable_thinking"] != false || body["response_format"].(map[string]any)["type"] != "json_object" {
			t.Error("JSON mode/nonthinking not preserved")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": finish, "message": map[string]string{"role": "assistant", "content": "{\"patches\":[]}"}}}})
	}))
	defer server.Close()
	thinking := false
	c, err := New(Config{BaseURL: server.URL + "/compatible-mode/v1/", APIKey: "test-key", Model: "test-model", Timeout: time.Second, MaxTokens: 128, EnableThinking: &thinking})
	if err != nil {
		t.Fatal(err)
	}
	text, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "json"}}, true)
	if err != nil || text != "{\"patches\":[]}" {
		t.Fatalf("%s %v", text, err)
	}
	finish = "length"
	if _, err = c.Complete(context.Background(), []Message{{Role: "user", Content: "json"}}, true); err == nil {
		t.Fatal("accepted truncated response")
	}
}

func TestProviderErrorDoesNotExposeSecretOrResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte("secret-test-key private-prompt"))
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, APIKey: "secret-test-key", Model: "test", Timeout: time.Second, MaxTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Complete(context.Background(), []Message{{Role: "user", Content: "private-prompt"}}, false)
	if err == nil || strings.Contains(err.Error(), "secret-test-key") || strings.Contains(err.Error(), "private-prompt") {
		t.Fatalf("unsafe provider error: %v", err)
	}
}
