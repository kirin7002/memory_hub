// 本文件验证真实 PostgreSQL 上的端云会话流程、状态采纳与冲突保护。
// httpCall 经过真实 HTTP Handler；s.Message 等调用直接验证服务层事务。
// 服务层调用旁标注的是对应公开接口，便于对照端侧接入流程；不会因此发出 HTTP 请求。
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"memoryhub/internal/auth"
	"memoryhub/internal/httpapi"
	"memoryhub/internal/hub"
	"memoryhub/internal/store"
)

var ctx = context.Background()
var key = []byte("integration-only-secret-32-bytes-or-more")

func principal(device string) auth.Principal {
	return auth.Principal{Tenant: "demo", User: "demo-user", Device: device, Agent: "demo-agent", Expires: time.Now().Add(time.Hour).Unix()}
}

// setup 为每项测试创建随机 Schema，通过 search_path 隔离数据，结束后删除。
// 迁移执行两次验证可重复性；Seed 只准备开发用户和设备，没有设备注册 HTTP 请求。
func setup(t *testing.T) *hub.Service {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set: PostgreSQL integration test skipped")
	}
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := hub.ID("test")
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); admin.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE"); admin.Close() })
	if err = store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(ctx, pool); err != nil {
		t.Fatal("migration replay:", err)
	}
	if err = store.Seed(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return &hub.Service{Pool: pool, Key: key, Lease: 30 * time.Second}
}

// conversation 创建带行程上下文的会话；对应 POST /v1/conversations。
func conversation(t *testing.T, s *hub.Service) hub.Conversation {
	t.Helper()
	r, err := s.CreateConversation(ctx, principal("phone"), hub.ID("create"), hub.CreateConversation{ID: hub.ID("conv"), Title: "行程", TaskType: "trip_planning"})
	if err != nil {
		t.Fatal(err)
	}
	return r.Value.(hub.Conversation)
}

// message 仅构造消息与模拟时间证据，不发送请求；Observed 表示端侧看到的上下文版本。
func message(device, id, origin string, seq hub.Seq, observed hub.Seq, text string, parents ...string) hub.Message {
	now := time.Now().UTC()
	return hub.Message{ID: id, Text: text, Observed: observed, Timezone: "Asia/Shanghai", Timing: hub.Timing{Schema: 1, Origin: origin, Boot: "boot", Segment: "segment", Stream: seq, Mono: seq * 1000, SourceTime: &now, Parents: parents}}
}
func errCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *hub.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

// claim 对应 POST /v1/device-agent/contexts/{id}/runs/claim，领取版本 v 的执行资格。
func claim(t *testing.T, s *hub.Service, p auth.Principal, c string, v hub.Seq) hub.Run {
	t.Helper()
	r, err := s.Claim(ctx, p, c, hub.RunRequest{ID: hub.ID("claim"), Expected: v})
	if err != nil {
		t.Fatal(err)
	}
	return r.Value.(hub.Run)
}

// interpret 对应 POST /v1/device-agent/runs/{id}/interpretations。
// 用预设目的地模拟模型候选，保留来源事件；确认或待确认由服务端规则决定。
func interpret(t *testing.T, s *hub.Service, p auth.Principal, run hub.Run, event, destination string) hub.Run {
	t.Helper()
	r, err := s.Interpret(ctx, p, run.ID, hub.Interpretation{RunRequest: hub.RunRequest{ID: hub.ID("interpret"), Expected: run.Version, Token: run.Token}, Inputs: []string{event}, Patches: []hub.Proposal{{Field: "destination", Value: destination, Sources: []string{event}}}})
	if err != nil {
		t.Fatal(err)
	}
	return r.Value.(hub.Run)
}

// httpCall 通过临时 HTTP 服务发送带设备凭证的请求，断言状态码并解析 JSON。
func httpCall(t *testing.T, server *httptest.Server, device, method, path string, body any, status int, headers map[string]string) map[string]any {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+auth.Sign(key, principal(device)))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("%s %s status=%d response=%v", method, path, resp.StatusCode, out)
	}
	return out
}

// TestHTTPPersistenceIdempotencyAndSync 验证手机写入后，车端能拉取且确认增量。
// 主流程：创建会话 → 快照 → 消息/重试 → 历史 → changes → ack → assemble。
func TestHTTPPersistenceIdempotencyAndSync(t *testing.T) {
	s := setup(t)
	server := httptest.NewServer(httpapi.New(s, key))
	defer server.Close()
	// POST /v1/conversations 与 POST /v1/sync/snapshot：建立会话并保存车端初始水位。
	created := httpCall(t, server, "phone", "POST", "/v1/conversations", hub.CreateConversation{ID: "trip", TaskType: "trip_planning"}, 200, map[string]string{"Idempotency-Key": "create-trip"})
	cid := created["value"].(map[string]any)["context_id"].(string)
	snap := httpCall(t, server, "car", "POST", "/v1/sync/snapshot", nil, 200, nil)
	msg := message("phone", "m1", "phone-trip", 1, 0, "目的地是上海")
	// POST /v1/conversations/{id}/messages：相同请求重试应返回同一提交水位。
	ack := httpCall(t, server, "phone", "POST", "/v1/conversations/trip/messages", msg, 200, nil)
	repeated := httpCall(t, server, "phone", "POST", "/v1/conversations/trip/messages", msg, 200, nil)
	if ack["user_seq"] != repeated["user_seq"] {
		t.Fatal("retry allocated another commit")
	}
	msg.Text = "目的地是杭州"
	// 同一 event_id 改正文应返回 409；GET /events 确认数据库只保存了一条记录。
	httpCall(t, server, "phone", "POST", "/v1/conversations/trip/messages", msg, 409, nil)
	history := httpCall(t, server, "car", "GET", "/v1/conversations/trip/events", nil, 200, nil)
	if len(history["events"].([]any)) != 1 {
		t.Fatal("duplicate event")
	}
	// 对应 GET /v1/sync/changes 与 POST /v1/sync/ack：拉取一项变更并确认已应用。
	changes, err := s.Changes(ctx, principal("car"), snap["cursor"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(changes["changes"].([]json.RawMessage)) != 1 {
		t.Fatalf("unexpected changes: %v", changes)
	}
	if err = s.Ack(ctx, principal("car"), changes["next_cursor"].(string)); err != nil {
		t.Fatal(err)
	}
	// 手机使用车端游标应被拒绝；游标绑定设备，不能跨端共享。
	_, err = s.Changes(ctx, principal("phone"), snap["cursor"].(string))
	errCode(t, err, "cursor_invalid")
	// POST /v1/contexts/{id}/assemble：尚未解释的手机输入必须出现在车端 Bundle。
	bundle := httpCall(t, server, "car", "POST", "/v1/contexts/"+cid+"/assemble", hub.AssembleRequest{}, 200, nil)
	if len(bundle["pending_inputs"].([]any)) != 1 {
		t.Fatal("pending message absent from Bundle")
	}
}

// TestSharedContextRepliesToTriggerConversation 验证共享任务下回复写回触发会话。
// 对应接口：POST /v1/conversations、POST /v1/conversations/{id}/messages、POST /v1/device-agent/runs/{id}/results。
// 读取使用 GET /v1/contexts/{id} 和 GET /v1/conversations/{id}/events；Run 调用见 claim/interpret 辅助函数。
func TestSharedContextRepliesToTriggerConversation(t *testing.T) {
	s := setup(t)
	p := principal("phone")
	first := conversation(t, s)
	// 第二个会话显式关联相同 Context；消息仍由第一个会话触发。
	created, err := s.CreateConversation(ctx, p, "bind-second", hub.CreateConversation{ID: "second", ContextID: first.ContextID})
	if err != nil {
		t.Fatal(err)
	}
	ack, err := s.Message(ctx, p, first.ID, message("phone", "trigger-first", "first-phone", 1, created.ContextVersion, "目的地是上海"))
	if err != nil {
		t.Fatal(err)
	}
	// GET /v1/contexts/{id}：车端可见手机的 pending 输入，解释后 pending 清空。
	c, err := s.GetContext(ctx, principal("car"), first.ContextID)
	if err != nil || len(c.State.PendingInputs) != 1 || c.State.PendingInputs[0] != "trigger-first" {
		t.Fatalf("pending inputs not visible across devices: %v %v", c.State.PendingInputs, err)
	}
	run := claim(t, s, p, first.ContextID, ack.ContextVersion)
	run = interpret(t, s, p, run, "trigger-first", "上海")
	c, err = s.GetContext(ctx, p, first.ContextID)
	if err != nil || len(c.State.PendingInputs) != 0 {
		t.Fatalf("processed input remained pending: %v %v", c.State.PendingInputs, err)
	}
	reply := message("phone", "reply-first", "first-phone", 2, run.Version, "已确认上海", "trigger-first")
	// POST /v1/device-agent/runs/{id}/results：回复应进入第一个会话，而非最近绑定的会话。
	_, err = s.Result(ctx, p, run.ID, hub.Result{RunRequest: hub.RunRequest{ID: "result-first", Expected: run.Version, Token: run.Token}, EventID: reply.ID, Text: reply.Text, Timing: reply.Timing})
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.Events(ctx, principal("car"), first.ID, 0)
	if err != nil || len(history["events"].([]hub.Event)) != 2 {
		t.Fatalf("reply not attached to trigger conversation: %v %v", history, err)
	}
	other, err := s.Events(ctx, p, "second", 0)
	if err != nil || len(other["events"].([]hub.Event)) != 0 {
		t.Fatalf("reply leaked into most recently bound conversation: %v %v", other, err)
	}
}

// TestRunFencingAndOwner 验证同一上下文只有一个有效在线执行，且执行资格绑定设备。
// 对应接口：POST /v1/conversations/{id}/messages、POST /v1/device-agent/runs/{id}/renew。
// Run 的领取与解释接口见 claim/interpret 辅助函数；验证结果使用 GET /v1/contexts/{id}。
func TestRunFencingAndOwner(t *testing.T) {
	s := setup(t)
	conv := conversation(t, s)
	p := principal("phone")
	m := message("phone", "m1", conv.ID+"-phone", 1, 0, "目的地是上海")
	ack, err := s.Message(ctx, p, conv.ID, m)
	if err != nil {
		t.Fatal(err)
	}
	run := claim(t, s, p, conv.ContextID, ack.ContextVersion)
	// 手机持有有效租约时，车端 claim 返回 run_busy；持有手机 token 也不能代手机续租。
	_, err = s.Claim(ctx, principal("car"), conv.ContextID, hub.RunRequest{ID: "second-claim", Expected: ack.ContextVersion})
	errCode(t, err, "run_busy")
	_, err = s.Renew(ctx, principal("car"), run.ID, hub.RunRequest{Expected: run.Version, Token: run.Token})
	errCode(t, err, "stale_run")
	run = interpret(t, s, p, run, "m1", "上海")
	c, err := s.GetContext(ctx, p, conv.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if c.State.Slots["destination"].Value != "上海" {
		t.Fatal("direct registered input not confirmed")
	}
	// 车端新消息使旧 Run 失效；手机继续提交 interpretations 应返回 stale_run。
	second := message("car", "m2", conv.ID+"-car", 1, c.Version, "改去杭州")
	if _, err = s.Message(ctx, principal("car"), conv.ID, second); err != nil {
		t.Fatal(err)
	}
	_, err = s.Interpret(ctx, p, run.ID, hub.Interpretation{RunRequest: hub.RunRequest{ID: "old-result", Expected: run.Version, Token: run.Token}, Inputs: []string{"m2"}})
	errCode(t, err, "stale_run")
}

// TestOfflineConflictReportedHistoryAndExplicitConfirmation 验证离线冲突直到用户确认的完整业务流程。
// 手机采纳上海 → 车端补传杭州 → 拒绝旧状态采纳 → 重新解释 → 用户确认杭州。
func TestOfflineConflictReportedHistoryAndExplicitConfirmation(t *testing.T) {
	s := setup(t)
	conv := conversation(t, s)
	p := principal("phone")
	// POST /v1/conversations/{id}/messages，再 claim/interpretations：先建立已确认上海状态。
	msg := message("phone", "phone-shanghai", conv.ID+"-phone", 1, 0, "目的地是上海")
	ack, err := s.Message(ctx, p, conv.ID, msg)
	if err != nil {
		t.Fatal(err)
	}
	run := claim(t, s, p, conv.ContextID, ack.ContextVersion)
	run = interpret(t, s, p, run, msg.ID, "上海")
	// POST /v1/device-agent/executions：原子保存旧版本用户输入及 reported 回复，不采纳字段。
	branch := hub.Execution{ID: "offline", Conversation: conv.ID, ContextID: conv.ContextID, Base: 0, Source: 1, Policy: 1, Events: []hub.OfflineEvent{
		{Message: message("car", "car-hangzhou", conv.ID+"-car", 1, 0, "目的地是杭州"), Type: "user_message"},
		{Message: message("car", "car-reply", conv.ID+"-car", 2, 0, "方案一：直达；方案二：途中休息", "car-hangzhou"), Type: "assistant_message"},
	}, Patches: []hub.Proposal{{Field: "destination", Value: "杭州", Sources: []string{"car-hangzhou"}}}}
	imported, err := s.Import(ctx, principal("car"), branch)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Value.(map[string]any)["state_applied"] != false {
		t.Fatal("import applied state")
	}
	// POST /v1/device-agent/executions/{id}/reconcile：基准过期，应要求重新校验。
	_, err = s.Reconcile(ctx, principal("car"), branch.ID, hub.RunRequest{ID: "reconcile-old", Expected: imported.ContextVersion})
	errCode(t, err, "execution_requires_revalidation")
	// POST /v1/contexts/{id}/assemble：即使旧建议未采纳，离线回复仍进入带标签历史。
	b, err := s.Assemble(ctx, p, conv.ContextID, hub.AssembleRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(b["reported_history"].([]hub.Event)) != 1 {
		t.Fatal("reported reply excluded")
	}
	// 新 claim/interpretations 在当前版本重解释；GET /contexts/{id} 应看到上海和杭州提案。
	run = claim(t, s, principal("car"), conv.ContextID, imported.ContextVersion)
	run = interpret(t, s, principal("car"), run, "car-hangzhou", "杭州")
	c, err := s.GetContext(ctx, p, conv.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if c.State.Slots["destination"].Value != "上海" || len(c.State.Proposals) != 1 {
		t.Fatalf("old branch overwrote state: %+v", c)
	}
	// POST /v1/contexts/{id}/commands（confirm_slot）：用户确认后才把目的地改为杭州。
	_, err = s.Command(ctx, p, conv.ContextID, hub.Command{ID: "confirm", Expected: c.Version, Name: "confirm_slot", ProposalID: c.State.Proposals[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.GetContext(ctx, p, conv.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if c.State.Slots["destination"].Value != "杭州" {
		t.Fatal("user confirmation not applied")
	}
}

// TestUnchangedOfflineBranchReconcilesOnce 验证未过期的离线分支可以采纳，重试只采纳一次。
// 对应接口：POST /v1/device-agent/executions、/v1/device-agent/executions/{id}/reconcile；GET /v1/contexts/{id}。
func TestUnchangedOfflineBranchReconcilesOnce(t *testing.T) {
	s := setup(t)
	conv := conversation(t, s)
	p := principal("car")
	branch := hub.Execution{ID: "offline-fresh", Conversation: conv.ID, ContextID: conv.ContextID, Base: 0, Source: 1, Policy: 1, Events: []hub.OfflineEvent{{Message: message("car", "m1", conv.ID+"-car", 1, 0, "目的地是杭州"), Type: "user_message"}}, Patches: []hub.Proposal{{Field: "destination", Value: "杭州", Sources: []string{"m1"}}}}
	imported, err := s.Import(ctx, p, branch)
	if err != nil {
		t.Fatal(err)
	}
	r := hub.RunRequest{ID: "fresh-reconcile", Expected: imported.ContextVersion}
	accepted, err := s.Reconcile(ctx, p, branch.ID, r)
	if err != nil {
		t.Fatal(err)
	}
	// 相同 request_id 和请求体重试：提交水位与上下文版本不能再次增长。
	again, err := s.Reconcile(ctx, p, branch.ID, r)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Seq != again.Seq {
		t.Fatal("retry changed state")
	}
	c, err := s.GetContext(ctx, p, conv.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if c.State.Slots["destination"].Value != "杭州" || c.Version != 2 {
		t.Fatalf("unexpected state: %+v", c)
	}
}

// TestAtomicRollbackAndConcurrentSequences 验证失败批次全回滚，以及手机/车端提交序号唯一。
// 对应接口：POST /v1/device-agent/executions、POST /v1/conversations/{id}/messages、GET /v1/conversations/{id}/events。
func TestAtomicRollbackAndConcurrentSequences(t *testing.T) {
	s := setup(t)
	conv := conversation(t, s)
	p := principal("phone")
	// 第二条事件引用不存在的父事件；即使第一条有效，整个 executions 批次也应回滚。
	batch := hub.Execution{ID: "bad-batch", Conversation: conv.ID, ContextID: conv.ContextID, Source: 1, Policy: 1, Events: []hub.OfflineEvent{
		{Message: message("phone", "would-be-written", conv.ID+"-phone", 1, 0, "目的地是上海"), Type: "user_message"},
		{Message: message("phone", "bad-parent", conv.ID+"-phone", 2, 0, "回复", "nonexistent"), Type: "assistant_message"},
	}}
	_, err := s.Import(ctx, p, batch)
	errCode(t, err, "parent_not_committed")
	h, err := s.Events(ctx, p, conv.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(h["events"].([]hub.Event)) != 0 {
		t.Fatal("failed batch partially committed")
	}
	// 同时调用两个设备的 messages，检查各自得到不同 user_seq 和对应的同步日志。
	var wg sync.WaitGroup
	results := make(chan hub.Receipt, 2)
	errs := make(chan error, 2)
	for _, device := range []string{"phone", "car"} {
		wg.Add(1)
		go func(d string) {
			defer wg.Done()
			r, err := s.Message(ctx, principal(d), conv.ID, message(d, "parallel-"+d, conv.ID+"-"+d, 1, 0, "问答"))
			if err != nil {
				errs <- err
			} else {
				results <- r
			}
		}(device)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	close(results)
	seen := map[hub.Seq]bool{}
	for r := range results {
		if seen[r.Seq] {
			t.Fatal("duplicate commit sequence")
		}
		seen[r.Seq] = true
	}
	if len(seen) != 2 {
		t.Fatal("missing result")
	}
	var count int
	err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM sync_changes WHERE tenant_id='demo' AND user_id='demo-user'`).Scan(&count)
	if err != nil || count != 3 {
		t.Fatalf("public mutations count %d: %v", count, err)
	}
}

// TestRevocationAndScopeIsolation 验证不同用户不可读取上下文，撤销设备不可继续提交。
// 对应接口：GET /v1/contexts/{id}、POST /v1/conversations/{id}/messages。
// 撤权通过直接更新测试数据库模拟；当前没有设备撤销 HTTP 接口。
func TestRevocationAndScopeIsolation(t *testing.T) {
	s := setup(t)
	conv := conversation(t, s)
	p := principal("phone")
	other := p
	other.User = "other-user"
	_, err := s.GetContext(ctx, other, conv.ContextID)
	errCode(t, err, "not_found")
	if _, err = s.Pool.Exec(ctx, `UPDATE devices SET status='revoked' WHERE tenant_id='demo' AND user_id='demo-user' AND device_id='phone'`); err != nil {
		t.Fatal(err)
	}
	_, err = s.Message(ctx, p, conv.ID, message("phone", "revoked", conv.ID+"-phone", 1, 0, "问答"))
	errCode(t, err, "not_found")
}

// TestCommitWaitsForEarlierUserTransaction 验证用户水位锁保证提交串行，不会越过未完成事务。
// 对应接口：POST /v1/conversations/{id}/messages；测试 SQL 先持锁，再验证写入等待与恢复。
func TestCommitWaitsForEarlierUserTransaction(t *testing.T) {
	s := setup(t)
	conv := conversation(t, s)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT committed_seq FROM user_stream_heads WHERE tenant_id='demo' AND user_id='demo-user' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.Message(ctx, principal("car"), conv.ID, message("car", "blocked", conv.ID+"-car", 1, 0, "问答"))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("writer passed locked watermark: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal(fmt.Errorf("writer failed to resume"))
	}
}

// TestFreshInstructionSupersedesUnresolvedProposal 验证新的明确输入替代同字段未确认推断。
// 对应接口：POST /v1/conversations/{id}/messages、POST /v1/contexts/{id}/commands、GET /v1/contexts/{id}。
// Run 的领取与解释接口见 claim/interpret 辅助函数。
func TestFreshInstructionSupersedesUnresolvedProposal(t *testing.T) {
	s := setup(t)
	conv := conversation(t, s)
	p := principal("phone")
	// 模型把模糊表达推断为杭州，规则应生成待确认提案。
	first := message("phone", "inferred", conv.ID+"-phone", 1, 0, "周末去江浙玩")
	ack, err := s.Message(ctx, p, conv.ID, first)
	if err != nil {
		t.Fatal(err)
	}
	run := claim(t, s, p, conv.ContextID, ack.ContextVersion)
	run = interpret(t, s, p, run, first.ID, "杭州")
	c, err := s.GetContext(ctx, p, conv.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	oldProposal := c.State.Proposals[0].ID
	// 用户明确说上海后重新解释；旧杭州提案及来源处理状态应被替代。
	next := message("phone", "explicit", conv.ID+"-phone", 2, c.Version, "目的地是上海", first.ID)
	ack, err = s.Message(ctx, p, conv.ID, next)
	if err != nil {
		t.Fatal(err)
	}
	run = claim(t, s, p, conv.ContextID, ack.ContextVersion)
	interpret(t, s, p, run, next.ID, "上海")
	c, err = s.GetContext(ctx, p, conv.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.State.Proposals) != 0 || c.State.Slots["destination"].Value != "上海" {
		t.Fatalf("proposal not superseded: %+v", c)
	}
	var resolution string
	var evidence []byte
	err = s.Pool.QueryRow(ctx, `SELECT resolution,evidence_event_ids FROM input_processing_revisions WHERE tenant_id='demo' AND user_id='demo-user' AND event_id=$1 ORDER BY user_seq DESC LIMIT 1`, first.ID).Scan(&resolution, &evidence)
	if err != nil || resolution != "superseded" {
		t.Fatalf("old input resolution=%s err=%v", resolution, err)
	}
	if !bytes.Contains(evidence, []byte(next.ID)) {
		t.Fatal("missing replacement evidence")
	}
	// commands(confirm_slot) 不允许再确认已经被替代的旧 proposal_id。
	_, err = s.Command(ctx, p, conv.ContextID, hub.Command{ID: "stale-proposal", Expected: c.Version, Name: "confirm_slot", ProposalID: oldProposal})
	errCode(t, err, "proposal_invalid")
}

// TestTimelineLateArrivalAndStablePagination 验证迟到消息仅进入新视图，旧分页保持固定水位。
// 对应接口：POST /v1/conversations/{id}/messages、GET /v1/conversations/{id}/timeline?cursor=...。
func TestTimelineLateArrivalAndStablePagination(t *testing.T) {
	s := setup(t)
	conv := conversation(t, s)
	p := principal("phone")
	previous := ""
	// 写入 101 条有因果前驱的事件，超过时间线每页 100 条，取得下一页游标。
	for i := 1; i <= 101; i++ {
		id := fmt.Sprintf("message-%03d", i)
		parents := []string{}
		if previous != "" {
			parents = append(parents, previous)
		}
		m := message("phone", id, conv.ID+"-phone", hub.Seq(i), hub.Seq(i-1), "问答", parents...)
		if _, err := s.Message(ctx, p, conv.ID, m); err != nil {
			t.Fatal(err)
		}
		previous = id
	}
	first, err := s.Timeline(ctx, p, conv.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	token := first["next_cursor"].(string)
	if token == "" {
		t.Fatal("expected timeline pagination")
	}
	// 模拟车端补传一小时前发生的事件：受理晚，但源时间早，独立于手机事件流。
	late := message("car", "late-earlier", conv.ID+"-car", 1, 0, "离线问题")
	earlier := time.Now().UTC().Add(-time.Hour)
	late.Timing.SourceTime = &earlier
	if _, err = s.Message(ctx, principal("car"), conv.ID, late); err != nil {
		t.Fatal(err)
	}
	// 使用旧 cursor 只返回旧水位剩余的第 101 条；不传 cursor 刷新才可见迟到事件。
	second, err := s.Timeline(ctx, p, conv.ID, token)
	if err != nil {
		t.Fatal(err)
	}
	ev := second["events"].([]hub.Event)
	if len(ev) != 1 || ev[0].ID != "message-101" || second["as_of_user_seq"] != first["as_of_user_seq"] {
		t.Fatalf("late arrival mutated old page: %v", second)
	}
	fresh, err := s.Timeline(ctx, p, conv.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	ev = fresh["events"].([]hub.Event)
	if ev[0].ID != late.ID || ev[0].TimeQuality != "estimated" {
		t.Fatalf("late event misplaced or falsely bounded: %+v", ev[0])
	}
	_, err = s.Timeline(ctx, principal("car"), conv.ID, token)
	errCode(t, err, "cursor_invalid")
}

// TestSnapshotIncludesUnboundContextAndTokenPrivacy 验证快照完整性、Run 凭证隔离和租约替换。
// 对应接口：POST /v1/contexts、POST /v1/sync/snapshot、POST /v1/device-agent/runs/{id}/renew。
// 查询/取消使用 GET /v1/runs/{id}、POST /v1/runs/{id}/cancel；领取接口见 claim 辅助函数。
func TestSnapshotIncludesUnboundContextAndTokenPrivacy(t *testing.T) {
	s := setup(t)
	p := principal("phone")
	// 尚未绑定会话的任务也应进入 sync/snapshot，避免签发游标后漏掉已有任务。
	created, err := s.CreateContext(ctx, p, "standalone", hub.CreateContext{ID: "unbound", TaskType: "trip_planning"})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(ctx, principal("car"))
	if err != nil {
		t.Fatal(err)
	}
	list := snap["contexts"].([]hub.Context)
	if len(list) != 1 || list[0].ID != created.Value.(hub.Context).ID {
		t.Fatal("snapshot cursor skipped unbound context")
	}
	conv := conversation(t, s)
	ack, err := s.Message(ctx, p, conv.ID, message("phone", "m1", conv.ID+"-phone", 1, 0, "问答"))
	if err != nil {
		t.Fatal(err)
	}
	run := claim(t, s, p, conv.ContextID, ack.ContextVersion)
	// GET /v1/runs/{id} 和 snapshot：另一端可见公开状态，不可取得 token 或租约 owner。
	public, err := s.GetRun(ctx, principal("car"), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if public.Token != 0 || public.Owner != "" {
		t.Fatal("public run disclosed execution credentials")
	}
	snap, err = s.Snapshot(ctx, principal("car"))
	if err != nil {
		t.Fatal(err)
	}
	active := snap["active_runs"].([]hub.Run)
	if len(active) != 1 || active[0].ID != run.ID || active[0].Token != 0 || active[0].Owner != "" {
		t.Fatalf("snapshot must restore public active Run without credentials: %+v", active)
	}
	// 直接把测试库租约更新为已过期，避免等待实际超时；renew 应拒绝，车端可领取替代 Run。
	if _, err = s.Pool.Exec(ctx, `UPDATE agent_runs SET lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1`, run.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.Renew(ctx, p, run.ID, hub.RunRequest{Expected: run.Version, Token: run.Token})
	errCode(t, err, "stale_run")
	replacement := claim(t, s, principal("car"), conv.ContextID, run.Version)
	if replacement.Token <= run.Token {
		t.Fatal("expired lease failed to fence old owner")
	}
	// POST /v1/runs/{id}/cancel：取消旧 ID 必须保持替代 Run 仍在运行。
	_, err = s.CancelRun(ctx, p, run.ID, hub.RunRequest{ID: "cancel-old", Expected: run.Version})
	if err != nil {
		t.Fatal(err)
	}
	public, err = s.GetRun(ctx, p, replacement.ID)
	if err != nil || public.Status != "running" {
		t.Fatal("old run cancellation cancelled its replacement")
	}
}

// TestExplicitConfirmationSupersedesQueuedEarlierInput 验证用户确认优先于已经排队的旧输入。
// 对应接口：POST /v1/conversations/{id}/messages、POST /v1/contexts/{id}/commands、GET /v1/contexts/{id}。
// Run 的领取与解释接口见 claim/interpret 辅助函数。
func TestExplicitConfirmationSupersedesQueuedEarlierInput(t *testing.T) {
	s := setup(t)
	conv := conversation(t, s)
	p := principal("phone")
	first := message("phone", "inferred", conv.ID+"-phone", 1, 0, "想去江浙")
	ack, err := s.Message(ctx, p, conv.ID, first)
	if err != nil {
		t.Fatal(err)
	}
	run := claim(t, s, p, conv.ContextID, ack.ContextVersion)
	interpret(t, s, p, run, first.ID, "杭州")
	c, err := s.GetContext(ctx, p, conv.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	proposal := c.State.Proposals[0].ID
	// 上海输入先提交但尚未解释；之后用户明确确认之前的杭州提案。
	older := message("phone", "queued-shanghai", conv.ID+"-phone", 2, c.Version, "目的地是上海", first.ID)
	ack, err = s.Message(ctx, p, conv.ID, older)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := s.Command(ctx, p, conv.ContextID, hub.Command{ID: "explicit-hangzhou", Expected: ack.ContextVersion, Name: "confirm_slot", ProposalID: proposal})
	if err != nil {
		t.Fatal(err)
	}
	// 确认后再处理队列中的上海输入：不得推翻杭州，应记录为 superseded。
	run = claim(t, s, p, conv.ContextID, confirmed.ContextVersion)
	interpret(t, s, p, run, older.ID, "上海")
	c, err = s.GetContext(ctx, p, conv.ContextID)
	if err != nil {
		t.Fatal(err)
	}
	if c.State.Slots["destination"].Value != "杭州" || len(c.State.Proposals) != 0 {
		t.Fatalf("queued input defeated explicit confirmation: %+v", c)
	}
	var status string
	err = s.Pool.QueryRow(ctx, `SELECT resolution FROM input_processing_revisions WHERE tenant_id='demo' AND user_id='demo-user' AND event_id='queued-shanghai' ORDER BY user_seq DESC LIMIT 1`).Scan(&status)
	if err != nil || status != "superseded" {
		t.Fatalf("queued input status=%s error=%v", status, err)
	}
}
