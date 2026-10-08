# 阿里云百炼真实模型接入

更新：2026-10-06。

项目支持网页双端演示和 `make chat`，通过百炼兼容 Chat Completions API 调用真实文本模型。网页由 Go 服务读取 Key 代理模型，见 [页面使用说明](web-demo-quickstart.md)。原 `make demo` 仍使用固定数据验证离线冲突。

## 1. 配置百炼

在百炼控制台开通模型服务并创建 API Key，见 [获取与配置 API Key](https://help.aliyun.com/zh/model-studio/get-api-key)。Key 的地域必须与访问地址匹配，见 [兼容 API 官方文档](https://help.aliyun.com/zh/model-studio/compatibility-of-openai-with-dashscope)。

在项目现有 `.env` 中补充以下配置，保留数据库与 MemoryHub 开发身份配置。新建配置时可复制 `.env.example`；不要用示例覆盖已有文件。

```dotenv
# 北京地域；通用域名仍可使用，业务空间专属域名见下文。
LLM_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1
LLM_MODEL=qwen-flash
LLM_API_KEY=填写你的百炼APIKey
LLM_TIMEOUT_SECONDS=120
LLM_MAX_TOKENS=1024
LLM_ENABLE_THINKING=false
# 启用同源手机 / 车端页面及 Go 模型代理。
WEB_DEMO_ENABLED=true
```

`LLM_BASE_URL` 只填写基础地址，客户端自动追加 `/chat/completions`。模型 ID 以百炼控制台实际开通的模型为准；示例使用非思考模式的 qwen-flash，并在行程字段提取时启用 JSON 模式，参考 [结构化输出文档](https://help.aliyun.com/zh/model-studio/qwen-structured-output)。当前入口非流式，不支持工具调用。

若使用北京业务空间专属域名，将地址改为：

```dotenv
LLM_BASE_URL=https://你的业务空间ID.cn-beijing.maas.aliyuncs.com/compatible-mode/v1
```

新加坡等其他地域使用该地域控制台给出的地址和 Key；不要用北京 Key 调用其他地域。域名形式和地域对应关系以官方兼容文档为准。

也支持官方变量 `DASHSCOPE_API_KEY`；如果两个 Key 都填写，优先使用 `LLM_API_KEY`。`.env` 已被忽略，不提交真实 Key；开发命令不输出 Key 或厂商原始错误响应。

## 2. 启动并对话

在项目目录执行：

```sh
make db
make migrate
make seed-demo
make run
```

打开 <http://127.0.0.1:18080/> 即可从页面使用；也可在另一个终端执行：

```sh
make chat
```

程序显示 `conversation_id` 和 `context_id`，随后支持多轮输入。例如：

```text
你：我喜欢杭州，旅行时更喜欢轻松的行程。
助手：（百炼实际生成的回复）
你：按照我刚才的偏好，推荐一种周末安排。
助手：（使用云端历史生成的回复）
你：/exit
```

每轮从 MemoryHub 获取历史；模型输出保存成功后才显示。`/exit` 退出，新开 `make chat` 默认创建新会话，不自动接续上次会话。

## 3. 手机与车端接续同一会话

复制第一端输出的会话 ID，在另一终端执行：

```sh
make chat ARGS='-device car -conversation conv_实际ID'
```

`-device phone/car` 是本地脚本模拟的设备身份；两端通过同一个 Go 云服务读取上下文，再由各自 Agent 进程调用百炼。当前并未安装原生手机 App 或车机程序。

也可只发送一条消息后退出：

```sh
make chat ARGS='-message "我喜欢杭州，请记住"'
make chat ARGS='-device car -conversation conv_实际ID -message "我刚才说喜欢哪里？"'
```

请按轮次交替发送；并发新输入会使旧 Run 失效，旧模型请求或结果可能被拒绝。这是当前冲突保护的实际行为。

## 4. 使用行程上下文

```sh
make chat ARGS='-task trip_planning'
```

输入 `目的地是上海` 后，本轮先请求一次模型提取字段，再请求一次模型生成回复。中间由服务端校验候选，回复模型读取校验后的 Bundle。

```text
读取 Context → 提交消息 → claim → assemble
→ 模型提取候选字段 → interpretations → 再次 assemble
→ 模型生成回复 → results
```

聊天类型没有字段提取，每轮只请求一次模型；行程类型每轮通常请求两次。`-task` 只影响新创建会话；复用会话时读取其现有任务类型。

目的地、绝对 RFC3339 出发时间是当前支持的字段。明确表达是否自动采纳仍由现有服务端规则决定；模型推断通常成为待确认提案。相对日期不自动转为已确认时间，模型也不会自动生成确认命令。

待确认项需要通过已有 `POST /v1/contexts/{id}/commands` 的 `confirm_slot` 命令确认。当前控制台没有确认菜单：可从 `GET /v1/contexts/{id}` 获取最新版本和 proposal_id，在 HTTP 客户端显式提交确认。

## 5. 接入位置与配置含义

```text
本机设备 Agent（cmd/chat）
  ├─ HTTP → MemoryHub → PostgreSQL
  └─ HTTP → 阿里云百炼 → 模型输出
```

`make chat` 由本机 Agent 进程读取模型 Key。启用网页时，Go 服务读取同一配置并通过 `/v1/demo/chat` 代理模型，浏览器只持有短期设备凭证，模型 Key 不进入前端。两种入口都复用已有 MemoryHub 数据与执行校验；未启用 `execution_target=cloud` 或后台云 Agent。

| 配置 | 用途 |
| --- | --- |
| `DATABASE_URL` | Go 云服务连接 PostgreSQL |
| `MEMORYHUB_URL` | 本机 Agent 请求 MemoryHub，示例为 `http://127.0.0.1:18080` |
| `DEV_AUTH_SECRET` | 本地开发设备凭证，与模型 Key 独立 |
| `LLM_BASE_URL` | 模型 API 基础地址 |
| `LLM_MODEL` | 百炼模型 ID |
| `LLM_API_KEY` / `DASHSCOPE_API_KEY` | 模型调用凭证 |
| `WEB_DEMO_ENABLED` | true 时开启同源双端页面、开发设备凭证入口及模型代理 |
| `LLM_TIMEOUT_SECONDS` | 单次模型请求超时，默认 120 秒，允许 1–600 秒 |
| `LLM_MAX_TOKENS` | 单次输出上限，默认 1024，当前允许 1–2048 |
| `LLM_ENABLE_THINKING` | 百炼思考模式参数，示例关闭；非流式入口建议使用支持非思考模式的文本模型 |

`make` 读取 `.env`；直接执行 `go run ./cmd/chat` 时需要自行传入环境变量。Bundle 仍使用原型的保守预算：5000 输入、2048 输出预留。内部 `mock-16k` 是已有预算配置标签，并不决定模型调用方式；实际厂商与模型由上述 LLM 配置决定。

## 6. 失败处理与验证范围

- 模型耗时期间每 10 秒续租；续租失效会取消模型请求，拒绝把旧执行结果保存为有效回复。
- Key/地域问题通常表现为 401/403；404 检查地址和模型；429 检查额度与限流。
- 截断输出、空回复、无效字段 JSON 或伪造来源均终止本轮。无效提取不会消费 pending 输入。
- 模型失败时尝试 `/fail` 结束本 Run，不自动重试付费请求。输入和此前已成功的解释可能已保存；最终回复失败不回滚整个对话流程。查询历史确认已有记录，再继续发送消息。
- 当前适配器只处理文本，无流式显示、图片、工具或真实离线模型；长历史仍受原型 Bundle 上限约束。

新增测试覆盖模型 API 请求格式、错误脱敏、租约丢失、字段来源校验，以及真实 HTTP + PostgreSQL 下的跨端多轮历史和行程双阶段处理。测试使用本地模型接口替身，不访问付费百炼：

```sh
make test
make test-integration
```

2026-10-06 已使用本地有效配置完成真实 qwen-flash 连通、手机/车端两轮会话接续和 PostgreSQL 持久化验证，见 [验证记录](real-model-smoke-test.md)。行程字段提取目前通过接口替身测试，本次没有对其进行付费服务实测。

代码：[真实模型入口](cmd/chat/main.go)、[模型客户端](internal/llm/client.go)、[设备 Agent 流程](internal/deviceagent/session.go)、[接入集成测试](tests/integration/llm_test.go)。
