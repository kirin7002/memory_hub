# Memory Hub：记忆系统调研与方案

更新日期：2026-10-06。

**Go 云侧原型已可运行。** 当前实现会话、上下文、端 Agent Run、离线导入/冲突、快照/增量/SSE 与时间线基础查询；完整覆盖情况见 [实现进度](implementation-status.md)，启动与真实 HTTP Demo 见 [运行说明](cloud-service-quickstart.md)。

```sh
cp .env.example .env # 仅首次配置；已有 .env 时保留原文件
make db
make migrate
make seed-demo
make run
# 配置 LLM_API_KEY 和 WEB_DEMO_ENABLED=true 后，打开 http://127.0.0.1:18080/。
```

**对话客户端已可使用。** 直接输入问题、持续追问、切换手机 / 车端身份；由 Go 服务代理真实模型并接入 MemoryHub，首条消息自动创建会话。使用与数据链路见 [对话客户端说明](web-demo-quickstart.md)。

**新增独立客户端：<http://127.0.0.1:18080/client>。** 多个页面分别连接不同账户和设备，支持本页断开/重连及补齐同步，见 [独立多页面客户端](independent-client-guide.md)。

三端独立入口：[手机](http://127.0.0.1:18080/phone?account=alice)、[车端](http://127.0.0.1:18080/car?account=alice)、[云端 Agent 模拟](http://127.0.0.1:18080/cloud?account=alice)。每端单独控制账户、会话和连接；云端入口使用现有 Go 代理，后台云执行器尚未实现。

当前方案分成云侧与端侧两个大块，手机和车端可以运行本地 Agent：**先详细设计与实施 Go 云侧服务，端侧后续单独做简单 Demo。**

**当前实施基线为 [一期设计 v2](phase1-memory-system-design-v2.md)**，已整合确认的业务选择、云侧流程、端侧契约及新增数据结构。旧文档作为补充资料；业务规则冲突时以 v2 为准。

| 入口 | 内容 |
| --- | --- |
| [运行说明](cloud-service-quickstart.md) | 本地 PostgreSQL、API、设备身份、端云 Demo 与测试 |
| [对话客户端](web-demo-quickstart.md) | 自由多轮对话、手机 / 车端切换、模型代理、历史与 Context 同步 |
| [独立多页面客户端](independent-client-guide.md) | 多账户、多窗口设备模拟、断开/重连、账户隔离及缓存 |
| [百炼真实模型接入](llm-setup.md) | Key/模型配置、make chat 多轮对话、跨端接续与行程字段提取 |
| [实现进度](implementation-status.md) | 已实现能力、实际验证、当前上限与未完成模块 |
| [已实现 API 契约](api/openapi.yaml) | 当前原型支持的接口与请求结构 |
| [一期设计 v2：当前基线](phase1-memory-system-design-v2.md) | 云侧详细设计、状态/输入处理规则、端云同步、时间校准、21 类表、API、Go 工程与验收；端侧简要设计 |
| [原一期设计总览](phase1-memory-system-design.md) | v1.3 范围与职责补充 |
| [原云侧详细设计](phase1-go-cloud-service-design.md) | 16 类核心 DDL、事务与 Go 示例；新版业务规则以 v2 为准 |
| [端侧补充方案](phase1-edge-demo-design.md) | 后续 Mock Demo 与 SQLite 增强；新版 reported 规则以 v2 为准 |

其他资料：

- [一期设计审查与决策依据](phase1-design-decision-review.md)：审查发现、八项已确认推荐选择与落地规则。
- [会话真实时间线设计](phase1-timeline-ordering-design.md)：提交顺序与展示分离、时钟校准、因果关系、离线补传与误差边界。
- [云侧开源仓库对比与复用建议](cloud-open-source-comparison.md)：独立服务候选与 Agent 框架参考分开，包含服务选型和 PoC 验收。
- [国内云数据库价格比较](cloud-database-price-comparison.md)：个人 Demo 云数据库候选、官方价格口径和来源。
- [三层总体调研与选型设计](memory-system-design.md)：长期演进与七个开源项目对比；早期实施建议以最新一期总览为准。
- [Reference 参考资料](reference/README.md)：官方来源与项目摘要。

当前云侧采用 Go、PostgreSQL 和数据库 Outbox，向端侧与云端 Agent 提供上下文；云端执行器可选。当前可用设备身份切换的对话客户端、真实模型命令行和固定数据 HTTP Demo；网页模型 Key 仅由 Go 服务持有。SQLite、持久队列与真实本地模型另行实现。长期画像、独立长期记忆引擎和图检索留到后续。
