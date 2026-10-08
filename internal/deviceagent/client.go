// Package deviceagent 是本机运行的设备 Agent 演示适配器；通过 HTTP 访问 MemoryHub。
package deviceagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"memoryhub/internal/auth"
	"memoryhub/internal/hub"
)

type Client struct {
	BaseURL string
	Key     []byte
	Device  string
	// Principal 由服务代理传入已验证身份；命令行入口省略时仍使用开发演示身份。
	Principal *auth.Principal
	HTTP      *http.Client
}

type HTTPError struct {
	Status int
	Code   string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("MemoryHub HTTP %d: %s", e.Status, e.Code) }

func (c *Client) Call(ctx context.Context, method, path string, body any, headers map[string]string, out any) error {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return errors.New("invalid MemoryHub URL")
	}
	p := auth.Principal{Tenant: "demo", User: "demo-user", Device: c.Device, Agent: "llm-demo"}
	if c.Principal != nil {
		p = *c.Principal
	}
	p.Expires = time.Now().Add(time.Hour).Unix()
	req.Header.Set("Authorization", "Bearer "+auth.Sign(c.Key, p))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("MemoryHub request failed (check service/network)")
	}
	defer resp.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil || len(raw) > 2*1024*1024 {
		return errors.New("invalid MemoryHub response size")
	}
	if resp.StatusCode != 200 {
		var e struct {
			Code string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return &HTTPError{Status: resp.StatusCode, Code: e.Code}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Conversation 创建新会话或按 ID 从列表中找到旧会话，使另一设备可接续相同历史。
func (c *Client) Conversation(ctx context.Context, id, task string) (hub.Conversation, error) {
	if id == "" {
		var receipt struct {
			Value hub.Conversation `json:"value"`
		}
		err := c.Call(ctx, "POST", "/v1/conversations", hub.CreateConversation{Title: "真实模型对话", TaskType: task}, map[string]string{"Idempotency-Key": hub.ID("create")}, &receipt)
		return receipt.Value, err
	}
	after := ""
	for {
		var page struct {
			Conversations []hub.Conversation `json:"conversations"`
			More          bool               `json:"has_more"`
			Next          string             `json:"next_after_id"`
		}
		if err := c.Call(ctx, "GET", "/v1/conversations?after_id="+url.QueryEscape(after), nil, nil, &page); err != nil {
			return hub.Conversation{}, err
		}
		for _, conv := range page.Conversations {
			if conv.ID == id {
				return conv, nil
			}
		}
		if !page.More {
			return hub.Conversation{}, errors.New("conversation not found")
		}
		if page.Next == "" || page.Next == after {
			return hub.Conversation{}, errors.New("invalid conversation pagination")
		}
		after = page.Next
	}
}
