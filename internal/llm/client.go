// Package llm 调用兼容 Chat Completions 的文本模型；不持有数据库写入权限。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Config struct {
	BaseURL        string
	APIKey         string
	Model          string
	Timeout        time.Duration
	MaxTokens      int
	EnableThinking *bool
}

// FromEnv 读取模型配置；DASHSCOPE_API_KEY 兼容百炼官方环境变量名称。
func FromEnv() (Config, error) {
	c := Config{BaseURL: os.Getenv("LLM_BASE_URL"), APIKey: os.Getenv("LLM_API_KEY"), Model: os.Getenv("LLM_MODEL"), Timeout: 120 * time.Second, MaxTokens: 1024}
	if c.APIKey == "" {
		c.APIKey = os.Getenv("DASHSCOPE_API_KEY")
	}
	if value := os.Getenv("LLM_TIMEOUT_SECONDS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 600 {
			return c, errors.New("LLM_TIMEOUT_SECONDS must be 1..600")
		}
		c.Timeout = time.Duration(n) * time.Second
	}
	if value := os.Getenv("LLM_MAX_TOKENS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 2048 {
			return c, errors.New("LLM_MAX_TOKENS must be 1..2048 (current Bundle reserve)")
		}
		c.MaxTokens = n
	}
	if value := os.Getenv("LLM_ENABLE_THINKING"); value != "" {
		b, err := strconv.ParseBool(value)
		if err != nil {
			return c, errors.New("LLM_ENABLE_THINKING must be true or false")
		}
		c.EnableThinking = &b
	}
	return c, nil
}

type Client struct {
	cfg      Config
	endpoint string
	http     *http.Client
}

func New(c Config) (*Client, error) {
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("LLM_BASE_URL must be an http(s) base URL without credentials, query or fragment")
	}
	if c.Model == "" {
		return nil, errors.New("LLM_MODEL is required")
	}
	if c.APIKey == "" {
		return nil, errors.New("set LLM_API_KEY or DASHSCOPE_API_KEY")
	}
	if c.Timeout <= 0 || c.MaxTokens < 1 || c.MaxTokens > 2048 {
		return nil, errors.New("invalid LLM timeout or output limit")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(u.Path, "/chat/completions") {
		return nil, errors.New("LLM_BASE_URL must omit /chat/completions; the client appends it")
	}
	u.Path += "/chat/completions"
	return &Client{cfg: c, endpoint: u.String(), http: &http.Client{Timeout: c.Timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Complete 发一次非流式模型请求；jsonMode 用于行程字段提取。
// 不自动重试，避免在结果未知时产生重复付费；错误不会输出 Key、提示词或厂商原始响应。
func (c *Client) Complete(ctx context.Context, messages []Message, jsonMode bool) (string, error) {
	body := struct {
		Model          string            `json:"model"`
		Messages       []Message         `json:"messages"`
		Stream         bool              `json:"stream"`
		MaxTokens      int               `json:"max_tokens"`
		EnableThinking *bool             `json:"enable_thinking,omitempty"`
		ResponseFormat map[string]string `json:"response_format,omitempty"`
	}{Model: c.cfg.Model, Messages: messages, MaxTokens: c.cfg.MaxTokens, EnableThinking: c.cfg.EnableThinking}
	if jsonMode {
		body.ResponseFormat = map[string]string{"type": "json_object"}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", errors.New("encode LLM request failed")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return "", errors.New("build LLM request failed")
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("LLM request failed (check endpoint, network and timeout)")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM HTTP %d (401/403: Key or region; 404: endpoint/model; 429: quota/rate limit)", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil {
		return "", errors.New("read LLM response failed")
	}
	if len(raw) > 1024*1024 {
		return "", errors.New("LLM response exceeded 1 MiB")
	}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
			Finish  string  `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &out) != nil || len(out.Choices) != 1 {
		return "", errors.New("invalid LLM response: expected one text choice")
	}
	if out.Choices[0].Finish != "stop" {
		return "", errors.New("LLM did not complete a text answer (truncated, filtered or tool response)")
	}
	answer := strings.TrimSpace(out.Choices[0].Message.Content)
	if answer == "" || len(answer) > 32*1024 {
		return "", errors.New("LLM answer is empty or exceeds MemoryHub event limit")
	}
	return answer, nil
}
