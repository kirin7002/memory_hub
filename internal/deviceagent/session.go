package deviceagent

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"memoryhub/internal/hub"
	"memoryhub/internal/llm"
)

type Model interface {
	Complete(context.Context, []llm.Message, bool) (string, error)
}
type Session struct {
	Hub          *Client
	Model        Model
	Conversation hub.Conversation
	Boot         string
}

func (s *Session) assemble(ctx context.Context, run hub.Run) (Bundle, error) {
	var b Bundle
	err := s.Hub.Call(ctx, "POST", "/v1/contexts/"+url.PathEscape(run.ContextID)+"/assemble", hub.AssembleRequest{Profile: "mock-16k", Budget: 5000, Reserve: 2048, RunID: run.ID, Expected: run.Version}, nil, &b)
	return b, err
}

// withLease 每 10 秒请求 POST /v1/device-agent/runs/{id}/renew。
// 续租失败立即取消模型请求；返回前等待续租协程结束，避免与解释产生的新版本竞争。
func (s *Session) withLease(ctx context.Context, run hub.Run, fn func(context.Context) (string, error)) (string, error) {
	return withLease(ctx, 10*time.Second, func(c context.Context) error {
		return s.Hub.Call(c, "POST", "/v1/device-agent/runs/"+url.PathEscape(run.ID)+"/renew", hub.RunRequest{Expected: run.Version, Token: run.Token}, nil, nil)
	}, fn)
}

func withLease(ctx context.Context, interval time.Duration, renew func(context.Context) error, fn func(context.Context) (string, error)) (string, error) {
	modelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				done <- nil
				return
			case <-modelCtx.Done():
				done <- modelCtx.Err()
				return
			case <-ticker.C:
				renewCtx, end := context.WithTimeout(modelCtx, 5*time.Second)
				err := renew(renewCtx)
				end()
				if err != nil {
					cancel()
					done <- fmt.Errorf("Run lease lost: %w", err)
					return
				}
			}
		}
	}()
	answer, err := fn(modelCtx)
	close(stop)
	if leaseErr := <-done; leaseErr != nil {
		return "", leaseErr
	}
	return answer, err
}

// Turn 完成一次真实模型交互；所有状态写入通过云端接口，模型没有数据库权限。
func (s *Session) Turn(ctx context.Context, text string) (answer string, err error) {
	if text == "" || len(text) > 32*1024 {
		return "", errors.New("message must be 1..32768 bytes")
	}
	var current hub.Context
	// GET /v1/contexts/{id}：提交前获取上下文版本与任务类型。
	if err = s.Hub.Call(ctx, "GET", "/v1/contexts/"+url.PathEscape(s.Conversation.ContextID), nil, nil, &current); err != nil {
		return "", err
	}
	if current.State.TaskType != "chat" && current.State.TaskType != "trip_planning" {
		return "", errors.New("unsupported task type")
	}
	// 每轮独立 origin 避免重启后的序号重用；同轮输入/回复使用 1/2 并建立因果引用。
	started := time.Now()
	origin := hub.ID("origin")
	eventID := hub.ID("input")
	timing := func(seq hub.Seq, parents ...string) hub.Timing {
		now := time.Now().UTC()
		return hub.Timing{Schema: 1, Origin: origin, Boot: s.Boot, Segment: s.Boot, Stream: seq, Mono: hub.Seq(time.Since(started).Nanoseconds()), SourceTime: &now, Parents: parents}
	}
	msg := hub.Message{ID: eventID, Text: text, Observed: current.Version, Timezone: "Asia/Shanghai", Timing: timing(1)}
	var ack hub.Receipt
	// POST /v1/conversations/{id}/messages：保存输入，随后 claim 使用 ACK 返回的版本。
	if err = s.Hub.Call(ctx, "POST", "/v1/conversations/"+url.PathEscape(s.Conversation.ID)+"/messages", msg, nil, &ack); err != nil {
		return "", err
	}
	var claimed struct {
		Value hub.Run `json:"value"`
	}
	if err = s.Hub.Call(ctx, "POST", "/v1/device-agent/contexts/"+url.PathEscape(current.ID)+"/runs/claim", hub.RunRequest{ID: hub.ID("claim"), Expected: ack.ContextVersion}, nil, &claimed); err != nil {
		return "", err
	}
	run := claimed.Value
	defer func() {
		if err != nil {
			// 失败时尝试 POST /fail 释放本 Run；失效 Run 由服务端拒绝，不能取消别人的新 Run。
			cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			_ = s.Hub.Call(cleanup, "POST", "/v1/device-agent/runs/"+url.PathEscape(run.ID)+"/fail", hub.RunRequest{ID: hub.ID("fail"), Expected: run.Version, Token: run.Token}, nil, nil)
		}
	}()
	bundle, err := s.assemble(ctx, run)
	if err != nil {
		return "", err
	}
	inputs := []string{}
	for _, e := range bundle.Pending {
		inputs = append(inputs, e.ID)
	}
	patches := []hub.Proposal{}
	if bundle.State.TaskType == "trip_planning" {
		// 百炼 JSON 模式提取候选字段；来源与字段先本地校验，再交给云端规则。
		var raw string
		raw, err = s.withLease(ctx, run, func(c context.Context) (string, error) { return s.Model.Complete(c, ExtractMessages(bundle), true) })
		if err != nil {
			return "", err
		}
		patches, err = ParsePatches(raw, bundle)
		if err != nil {
			return "", err
		}
	}
	// POST /interpretations：消费 pending 输入；聊天无字段候选，行程由服务器决定采纳/提案。
	var interpreted struct {
		Value hub.Run `json:"value"`
	}
	if err = s.Hub.Call(ctx, "POST", "/v1/device-agent/runs/"+url.PathEscape(run.ID)+"/interpretations", hub.Interpretation{RunRequest: hub.RunRequest{ID: hub.ID("interpret"), Expected: run.Version, Token: run.Token}, Inputs: inputs, Patches: patches}, nil, &interpreted); err != nil {
		return "", err
	}
	run = interpreted.Value
	// 再次 assemble：让回复模型看到服务器校验后的 confirmed_state 与待确认提案。
	bundle, err = s.assemble(ctx, run)
	if err != nil {
		return "", err
	}
	// 时钟偏差或预算截断可能改变历史尾部；显式指定本轮输入，缺失时拒绝误答其他问题。
	included := false
	for _, id := range bundle.Manifest.IDs {
		included = included || id == eventID
	}
	if !included {
		return "", errors.New("current input omitted from Bundle; cannot generate a grounded reply")
	}
	answer, err = s.withLease(ctx, run, func(c context.Context) (string, error) {
		return s.Model.Complete(c, ReplyMessages(bundle, eventID), false)
	})
	if err != nil {
		return "", err
	}
	// POST /results：只有保存成功才向控制台显示回复；期间发生新输入会拒绝旧结果。
	err = s.Hub.Call(ctx, "POST", "/v1/device-agent/runs/"+url.PathEscape(run.ID)+"/results", hub.Result{RunRequest: hub.RunRequest{ID: hub.ID("result"), Expected: run.Version, Token: run.Token}, EventID: hub.ID("reply"), Text: answer, Timing: timing(2, eventID)}, nil, nil)
	if err != nil {
		return "", err
	}
	return answer, nil
}
