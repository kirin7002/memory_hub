# 云侧原型运行说明

日期：2026-10-06 ｜ 适用：当前 0.1.0 云侧原型。完整覆盖情况见 [实现进度](implementation-status.md)。

## 1. 本地启动

需要 Go 1.26+ 与可用的 Docker。项目仅依赖一个 PostgreSQL 开发容器，默认数据库端口 55432、API 端口 18080，均绑定本机。

```sh
cp .env.example .env
make db
make migrate
make seed-demo
make run
```

如果已有自己的 `.env`，保留现有文件并按示例补配置。迁移不会自动在 API 启动时执行；重复 `make migrate` 校验已有迁移，不重建数据库。

开发身份固定为 tenant=demo、user=demo-user、设备 phone/car。开发凭证与示例密钥仅用于本地运行；服务必须显式 `DEV_AUTH_ENABLED=true` 才使用这套鉴权。当前没有生产身份模式。

运行后：

- 存活：`http://127.0.0.1:18080/livez`。
- 就绪：`http://127.0.0.1:18080/readyz`。
- 手机 / 车端页面：`http://127.0.0.1:18080/`；设置 `WEB_DEMO_ENABLED=true` 和模型配置后可真实对话，见 [页面 Demo](web-demo-quickstart.md)。
- 契约：[api/openapi.yaml](api/openapi.yaml)，使用 JSON 语法书写，兼容 YAML 1.2，只列已实现接口。

## 2. 运行端云接续 Demo

需要真实调用百炼模型时，按 [真实模型接入说明](llm-setup.md) 配置并执行 `make chat`；下述 `make demo` 保留固定数据场景，便于重复验证离线冲突。

API 启动后，在另一个终端执行：

```sh
make demo
```

驱动通过真实 HTTP 模拟手机/车端 Agent，不调用云端大模型。每次创建新行程，验证：

1. 手机提交“目的地是上海”，重复发送得到同一提交水位。
2. 手机领取 Run，提交来源提案并保存最终回复。
3. 车端上传基于旧上下文的“杭州”分支和两个候选方案的回复。
4. 历史已保存，旧建议 reconcile 返回 execution_requires_revalidation。
5. 另一端 Bundle 可读取 reported 回复，上海目标保持。
6. 新 Run 重解释杭州输入，生成待确认提案；时间线可读取。

成功输出应包含：

```json
{
  "current_destination": "上海",
  "duplicate_message_deduplicated": true,
  "offline_records_persisted": true,
  "offline_state_automatically_applied": false,
  "pending_proposal": "杭州",
  "reconcile_error": "execution_requires_revalidation",
  "reported_reply_in_bundle": true,
  "timeline_absolute_order_guaranteed": false
}
```

驱动不会自动确认杭州；这用于展示“历史完整保存”和“状态采纳”分开。数据持久保存在本项目 Docker volume，容器重启不会自动清空。

## 3. 手动调用 API

先通过以下命令取得设备开发 Token，再在 Postman 或 HTTP 客户端中使用 `Authorization: Bearer ...`：

```sh
make token-phone
make token-car
```

创建行程会话：`POST /v1/conversations`，Header 传 `Idempotency-Key`。

```json
{
  "title": "杭州行程",
  "task_type": "trip_planning"
}
```

返回 value 中的 conversation_id/context_id 用于后续请求。普通聊天省略 task_type 或传 chat；要接续既有任务，在创建会话时传 context_id。

提交用户消息：`POST /v1/conversations/{id}/messages`。

```json
{
  "event_id": "message-phone-001",
  "text": "目的地是上海",
  "observed_context_version": "0",
  "timezone": "Asia/Shanghai",
  "offline": false,
  "timing": {
    "schema_version": 1,
    "origin_id": "phone-trip-01",
    "boot_id": "boot-01",
    "clock_segment_id": "clock-01",
    "stream_seq": "1",
    "monotonic_ns": "1000000000",
    "source_time": "2026-10-01T10:00:00+08:00",
    "causal_parent_event_ids": []
  }
}
```

首次提交后的下一事件沿用已登记 origin，stream_seq 递增并引用本流前驱 event_id；不同任务或 producer 使用独立 origin。请求重试保留原 event_id、正文和时序证据，不能每次重试更新时间。

所有版本、序号、token 在 JSON 中为十进制字符串。读取上下文：`GET /v1/contexts/{id}`；组装：`POST /v1/contexts/{id}/assemble`，示例：

```json
{
  "model_profile_id": "car-local-8k",
  "input_budget_tokens": 5000,
  "output_reserve_tokens": 2048
}
```

在线 Agent 需先 claim，再以匹配的 accepted_context_version/token 提交 interpretations/results。新输入、用户命令或租约失效后，旧结果拒绝。无有效租约的本地历史走 executions 导入。

## 4. 测试与开发

```sh
make build
make test
make test-integration
make vet
```

集成测试在 TEST_DATABASE_URL 中创建并清理随机 Schema；使用项目开发数据库或单独测试库，不使用生产库。测试覆盖事务、身份、跨端同步、冲突、确认和时间线。

`.env` 由 Makefile 读取；直接使用 `go run/go test` 时需要自行传环境变量。API 通过 Ctrl+C 优雅关闭，PostgreSQL 可通过以下命令停止，保留已有 volume：

```sh
docker compose -p memoryhub -f deploy/compose.yaml stop
```

不要修改已经应用的迁移；新增变化通过后续编号 SQL 文件提交。当前模块职责和后续工作见 [实现进度](implementation-status.md) 与 [设计 v2](phase1-memory-system-design-v2.md)。
