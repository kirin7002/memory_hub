// 本程序通过真实 HTTP 请求模拟手机与车端的在线处理、离线补传和冲突重解释。
// 前置条件：启动 PostgreSQL、执行迁移与 seed-demo，并启动云服务；使用 make demo 运行。
// 消息和模型输出是预设数据；接口、鉴权、数据库事务和冲突校验均真实执行。
// 场景终点是产生杭州待确认提案；用户确认的后续流程由集成测试覆盖。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"memoryhub/internal/auth"
	"memoryhub/internal/hub"
	"net/http"
	"os"
	"strings"
	"time"
)

type client struct {
	url  string
	key  []byte
	http *http.Client
}

// call 使用指定设备的开发身份发请求，并断言 HTTP 状态码；409 等预期拒绝也可验证。
func (c client) call(device, method, path string, body any, headers map[string]string, status int) map[string]any {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, c.url+path, bytes.NewReader(b))
	check(err)
	req.Header.Set("Authorization", "Bearer "+auth.Sign(c.key, auth.Principal{Tenant: "demo", User: "demo-user", Device: device, Agent: "demo-agent", Expires: time.Now().Add(time.Hour).Unix()}))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	check(err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	check(err)
	if resp.StatusCode != status {
		panic(fmt.Sprintf("%s %s: status=%d body=%s", method, path, resp.StatusCode, raw))
	}
	var value map[string]any
	check(json.Unmarshal(raw, &value))
	return value
}
func check(err error) {
	if err != nil {
		panic(err)
	}
}
func convert[T any](v any) T {
	var t T
	b, err := json.Marshal(v)
	check(err)
	check(json.Unmarshal(b, &t))
	return t
}

// msg 构造事件时间证据：同一 origin 的 seq 递增，parents 描述因果前驱。
// observed 是生成事件时看到的上下文版本；模拟时间只用于验证协议，不代表实际设备时钟。
func msg(id, origin, text string, seq, observed hub.Seq, parents ...string) hub.Message {
	now := time.Now().UTC()
	return hub.Message{ID: id, Text: text, Observed: observed, Offline: false, Timezone: "Asia/Shanghai", Timing: hub.Timing{Schema: 1, Origin: origin, Boot: "demo-boot", Segment: "demo-segment", Stream: seq, Mono: seq * 1000, SourceTime: &now, Parents: parents}}
}

func main() {
	secret := os.Getenv("DEV_AUTH_SECRET")
	if len(secret) < 32 {
		fmt.Fprintln(os.Stderr, "DEV_AUTH_SECRET (32+ bytes) is required")
		os.Exit(1)
	}
	url := os.Getenv("MEMORYHUB_URL")
	if url == "" {
		url = "http://127.0.0.1:8080"
	}
	c := client{strings.TrimRight(url, "/"), []byte(secret), &http.Client{Timeout: 10 * time.Second}}

	// 步骤 1：POST /v1/conversations，创建行程会话及其任务上下文。
	// Idempotency-Key 标识创建请求；返回的会话和上下文 ID 用于后续端云接续。
	create := c.call("phone", "POST", "/v1/conversations", hub.CreateConversation{TaskType: "trip_planning", Title: "端云接续 Demo"}, map[string]string{"Idempotency-Key": hub.ID("create")}, 200)
	conv := convert[hub.Conversation](create["value"])

	// 步骤 2：模拟车端在版本 0 上离线生成杭州输入和回复；此时不请求接口。
	// 回复引用用户输入，两个事件先保留在脚本内存，手机更新目标后才上传。
	localInput := msg(hub.ID("car_input"), conv.ID+"-car", "目的地是杭州", 1, 0)
	localReply := msg(hub.ID("car_reply"), conv.ID+"-car", "方案一：直达；方案二：途中休息", 2, 0, localInput.ID)

	// 步骤 3：POST /v1/sync/snapshot，以车端身份获取初始快照与设备绑定游标。
	// 保存游标，之后从同一同步水位拉取手机提交及车端上传产生的变更。
	snapshot := c.call("car", "POST", "/v1/sync/snapshot", nil, nil, 200)
	phoneInput := msg(hub.ID("phone_input"), conv.ID+"-phone", "目的地是上海", 1, 0)

	// 步骤 4：POST /v1/conversations/{id}/messages，保存手机输入并生成 pending 输入。
	// 用完全相同的事件重试，断言 user_seq 不变；此时仅存记录，字段尚未采纳。
	ack := c.call("phone", "POST", "/v1/conversations/"+conv.ID+"/messages", phoneInput, nil, 200)
	repeated := c.call("phone", "POST", "/v1/conversations/"+conv.ID+"/messages", phoneInput, nil, 200)
	if ack["user_seq"] != repeated["user_seq"] {
		panic("retry was not idempotent")
	}

	// 步骤 5：POST /v1/device-agent/contexts/{id}/runs/claim，领取手机在线执行。
	// 传消息 ACK 后的版本；返回的 token 与租约用于限制后续写入的设备及执行资格。
	run := convert[hub.Run](c.call("phone", "POST", "/v1/device-agent/contexts/"+conv.ContextID+"/runs/claim", hub.RunRequest{ID: hub.ID("claim"), Expected: convert[hub.Seq](ack["context_version"])}, nil, 200)["value"])

	// 步骤 6：POST /v1/device-agent/runs/{id}/interpretations，提交目的地及来源输入。
	// “目的地是上海”满足服务端明确表达规则；使用返回的新版本继续提交最终回复。
	interp := hub.Interpretation{RunRequest: hub.RunRequest{ID: hub.ID("interpret"), Expected: run.Version, Token: run.Token}, Inputs: []string{phoneInput.ID}, Patches: []hub.Proposal{{Field: "destination", Value: "上海", Sources: []string{phoneInput.ID}}}}
	run = convert[hub.Run](c.call("phone", "POST", "/v1/device-agent/runs/"+run.ID+"/interpretations", interp, nil, 200)["value"])
	reply := msg(hub.ID("phone_reply"), conv.ID+"-phone", "已记录上海行程", 2, run.Version, phoneInput.ID)

	// 步骤 7：POST /v1/device-agent/runs/{id}/results，保存手机最终回复并结束 Run。
	// 本例直接提交预设回复，没有实际调用模型，也没有演示在线处理前的 assemble。
	c.call("phone", "POST", "/v1/device-agent/runs/"+run.ID+"/results", hub.Result{RunRequest: hub.RunRequest{ID: hub.ID("result"), Expected: run.Version, Token: run.Token}, EventID: reply.ID, Text: reply.Text, Timing: reply.Timing}, nil, 200)

	// 步骤 8：POST /v1/device-agent/executions，模拟车端恢复网络后整批上传旧分支。
	// Base/Source/Policy 是离线生成时的基准；回复以 reported 保存，上传不自动采纳杭州。
	branch := hub.Execution{ID: hub.ID("execution"), Conversation: conv.ID, ContextID: conv.ContextID, Base: 0, Source: 1, Policy: 1, Events: []hub.OfflineEvent{{Message: localInput, Type: "user_message"}, {Message: localReply, Type: "assistant_message"}}, Patches: []hub.Proposal{{Field: "destination", Value: "杭州", Sources: []string{localInput.ID}}}}
	imported := c.call("car", "POST", "/v1/device-agent/executions", branch, nil, 200)

	// 步骤 9：POST /v1/device-agent/executions/{id}/reconcile，尝试采纳旧分支建议。
	// 手机已经更新上下文，预期 HTTP 409；拒绝采纳不会删除刚保存的离线历史。
	rejected := c.call("car", "POST", "/v1/device-agent/executions/"+branch.ID+"/reconcile", hub.RunRequest{ID: hub.ID("reconcile"), Expected: convert[hub.Seq](imported["context_version"])}, nil, 409)

	// 步骤 10：GET /v1/sync/changes?cursor=...，验证车端旧游标可请求后续增量。
	// 本例检查接口成功；增量内容和 ACK 的断言在 TestHTTPPersistenceIdempotencyAndSync 中。
	c.call("car", "GET", "/v1/sync/changes?cursor="+snapshot["cursor"].(string), nil, nil, 200)

	// 步骤 11：POST /v1/contexts/{id}/assemble，获取车端可使用的 Context Bundle。
	// 再 GET /v1/contexts/{id} 检查状态：目的地仍为上海，reported_history 包含离线回复。
	bundle := c.call("car", "POST", "/v1/contexts/"+conv.ContextID+"/assemble", hub.AssembleRequest{}, nil, 200)
	state := convert[hub.Context](c.call("phone", "GET", "/v1/contexts/"+conv.ContextID, nil, nil, 200))
	if state.State.Slots["destination"].Value != "上海" {
		panic("old branch overwrote the phone goal")
	}
	if len(bundle["reported_history"].([]any)) != 1 {
		panic("reported reply absent")
	}

	// 步骤 12：POST /v1/device-agent/contexts/{id}/runs/claim，在最新版本上领取车端 Run。
	// 旧分支需要重新解释，不能继续沿用原离线执行的状态写入资格。
	run = convert[hub.Run](c.call("car", "POST", "/v1/device-agent/contexts/"+conv.ContextID+"/runs/claim", hub.RunRequest{ID: hub.ID("claim"), Expected: state.Version}, nil, 200)["value"])

	// 步骤 13：POST /v1/device-agent/runs/{id}/interpretations，重新解释杭州输入。
	// 随后 GET /v1/contexts/{id}：旧输入与上海冲突，应生成提案，等待用户明确确认。
	interp = hub.Interpretation{RunRequest: hub.RunRequest{ID: hub.ID("interpret"), Expected: run.Version, Token: run.Token}, Inputs: []string{localInput.ID}, Patches: branch.Patches}
	c.call("car", "POST", "/v1/device-agent/runs/"+run.ID+"/interpretations", interp, nil, 200)
	state = convert[hub.Context](c.call("phone", "GET", "/v1/contexts/"+conv.ContextID, nil, nil, 200))
	if state.State.Slots["destination"].Value != "上海" || len(state.State.Proposals) != 1 {
		panic("expected a clarification, not overwrite")
	}

	// 步骤 14：GET /v1/conversations/{id}/timeline，读取手机与车端合并后的时间线。
	// 检查接口成功并输出事件数；时间先后与固定水位分页由时间线测试断言。
	// 最后输出本场景结果；Demo 停在待确认阶段，保留车端 Run 等待用户操作或租约失效。
	timeline := c.call("car", "GET", "/v1/conversations/"+conv.ID+"/timeline", nil, nil, 200)
	out := map[string]any{"conversation_id": conv.ID, "context_id": conv.ContextID, "duplicate_message_deduplicated": true, "current_destination": state.State.Slots["destination"].Value, "offline_records_persisted": true, "offline_state_automatically_applied": false, "reconcile_error": rejected["error"], "reported_reply_in_bundle": true, "pending_proposal": state.State.Proposals[0].Value, "timeline_events": len(timeline["events"].([]any)), "timeline_absolute_order_guaranteed": false}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}
