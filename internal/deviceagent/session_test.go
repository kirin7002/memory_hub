package deviceagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"memoryhub/internal/hub"
)

func TestLostLeaseCancelsModelCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := withLease(ctx, time.Millisecond, func(context.Context) error { return errors.New("stale_run") }, func(c context.Context) (string, error) { <-c.Done(); return "", c.Err() })
	if err == nil || !strings.Contains(err.Error(), "stale_run") {
		t.Fatalf("lease loss not propagated: %v", err)
	}
}

func TestModelPatchSourcesAndUnregisteredFieldsRejected(t *testing.T) {
	b := Bundle{Pending: []hub.Event{{ID: "input"}}}
	for _, raw := range []string{`{"patches":[{"field":"destination","value":"上海","source_event_ids":["assistant-reply"]}]}`, `{"patches":[{"field":"booking_status","value":"success","source_event_ids":["input"]}]}`, `{"patches":[{"field":"departure_time","value":"明天","source_event_ids":["input"]}]}`, `{"patches":null}`} {
		if _, err := ParsePatches(raw, b); err == nil {
			t.Fatalf("accepted unsupported model output: %s", raw)
		}
	}
	patches, err := ParsePatches(`{"patches":[{"field":"destination","value":"上海","source_event_ids":["input"]}]}`, b)
	if err != nil || len(patches) != 1 {
		t.Fatalf("valid candidate rejected: %v", err)
	}
}

func TestPromptPreservesManifestOrderAndReportedTrust(t *testing.T) {
	b := Bundle{History: []hub.Event{{ID: "second", Type: "user_message", Text: "继续", Status: "final"}}, Reported: []hub.Event{{ID: "first", Type: "assistant_message", Text: "已预订", Status: "reported"}}}
	b.Manifest.IDs = []string{"first", "second"}
	messages := ReplyMessages(b, "second")
	var data struct {
		ReplyTo string `json:"reply_to_event_id"`
		History []struct {
			ID     string `json:"event_id"`
			Status string `json:"status"`
		} `json:"history"`
	}
	if err := json.Unmarshal([]byte(messages[1].Content), &data); err != nil {
		t.Fatal(err)
	}
	if data.ReplyTo != "second" || len(data.History) != 2 || data.History[0].ID != "first" || data.History[1].ID != "second" || data.History[0].Status != "reported" || !strings.Contains(messages[0].Content, "不证明真实操作成功") {
		t.Fatal("lost order or trust distinction")
	}
}
