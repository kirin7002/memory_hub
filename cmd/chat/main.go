// chat 模拟运行在设备上的真实模型 Agent；make chat 读取 .env 并启动多轮对话。
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"memoryhub/internal/deviceagent"
	"memoryhub/internal/hub"
	"memoryhub/internal/llm"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	device := flag.String("device", "phone", "phone or car")
	conversation := flag.String("conversation", "", "resume an existing conversation ID")
	task := flag.String("task", "chat", "chat or trip_planning (new conversation only)")
	message := flag.String("message", "", "send one message and exit; omit for interactive chat")
	flag.Parse()
	if (*device != "phone" && *device != "car") || (*task != "chat" && *task != "trip_planning") {
		return errors.New("device must be phone/car; task must be chat/trip_planning")
	}
	secret := os.Getenv("DEV_AUTH_SECRET")
	if len(secret) < 32 {
		return errors.New("DEV_AUTH_SECRET (32+ bytes) is required")
	}
	cfg, err := llm.FromEnv()
	if err != nil {
		return err
	}
	model, err := llm.New(cfg)
	if err != nil {
		return err
	}
	base := os.Getenv("MEMORYHUB_URL")
	if base == "" {
		base = "http://127.0.0.1:18080"
	}
	api := &deviceagent.Client{BaseURL: base, Key: []byte(secret), Device: *device, HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	conv, err := api.Conversation(ctx, *conversation, *task)
	if err != nil {
		return err
	}
	session := &deviceagent.Session{Hub: api, Model: model, Conversation: conv, Boot: hub.ID("boot")}
	fmt.Printf("device=%s model=%s\nconversation_id=%s\ncontext_id=%s\n", *device, cfg.Model, conv.ID, conv.ContextID)
	if *message != "" {
		reply, err := session.Turn(ctx, *message)
		if err != nil {
			return err
		}
		fmt.Println("助手：" + reply)
		return nil
	}
	fmt.Println("输入消息开始对话，输入 /exit 退出。换端时用 -conversation 复用上述会话 ID。")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 32*1024)
	lines := make(chan string)
	readErrors := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		readErrors <- scanner.Err()
	}()
	for {
		fmt.Print("你：")
		var text string
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErrors:
			return err
		case text = <-lines:
			text = strings.TrimSpace(text)
		}
		if text == "/exit" {
			break
		}
		if text == "" {
			continue
		}
		reply, err := session.Turn(ctx, text)
		if err != nil {
			fmt.Fprintln(os.Stderr, "本轮未完成：", err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		fmt.Println("助手：" + reply)
	}
	return nil
}
