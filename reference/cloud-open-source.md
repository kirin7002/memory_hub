# 云侧开源仓库调研 Reference

核查日期：2026-10-01。仅保存官方来源链接及核查用途，不复制完整第三方文档或源码。主报告：[云侧开源仓库对比](../cloud-open-source-comparison.md)。

## Acontext

| 来源 | 核查用途 |
| --- | --- |
| [仓库与 README](https://github.com/memodb-io/Acontext) | 开源定位、Skill Memory、自托管入口、主许可 |
| [Server 目录](https://github.com/memodb-io/Acontext/tree/main/src/server) | API、Core 与界面组成，混合技术栈 |
| [API 目录](https://github.com/memodb-io/Acontext/tree/main/src/server/api) | Go REST API 与相关组件 |
| [Go 模块](https://github.com/memodb-io/Acontext/blob/main/src/server/api/go/go.mod) | API 的 Go 实现及依赖 |
| [Docker Compose](https://github.com/memodb-io/Acontext/blob/main/src/server/docker-compose.yaml) | PostgreSQL、Redis、RabbitMQ、S3 兼容存储与 Core/API 部署关系；不能据此断言所有功能的最小部署要求 |
| [Context Editing](https://docs.acontext.io/engineering/editing) | 读取时上下文裁剪，保留原始存储消息 |
| [Task Tracking](https://docs.acontext.io/observe/whatis) | 从消息异步识别任务与进展；可关闭任务追踪 |
| [Session Summary](https://docs.acontext.io/engineering/session_summary) | 任务摘要和近期消息，异步摘要的时效限制 |

核查重点：独立服务与部署形态、会话保存、模型输入裁剪。尚未验证设备游标同步、业务事务统一写入与 Run fencing。

## tRPC-Agent-Go（框架参考，不是独立 Memory Hub）

| 来源 | 核查用途 |
| --- | --- |
| [仓库](https://github.com/trpc-group/trpc-agent-go) | Go 框架定位、Runner、工具和主许可 |
| [Session 文档](https://github.com/trpc-group/trpc-agent-go/blob/main/docs/mkdocs/en/session.md) | 用户/应用/会话作用域、存储后端、摘要、事件窗口、持久化选项 |
| [PostgreSQL 目录](https://github.com/trpc-group/trpc-agent-go/tree/main/session/postgres) | 默认数据库 Session 后端及测试文件 |
| [Schema](https://github.com/trpc-group/trpc-agent-go/blob/main/session/postgres/schema.sql) | session_states、session_events、session_summaries、app_states、user_states |
| [Service 源码](https://github.com/trpc-group/trpc-agent-go/blob/main/session/postgres/service.go) | AppendEvent、同步/异步持久化路径与 hook 边界；开启异步模式后入进程通道即可返回 |

核查重点：框架内 Go Session 能力与可选适配可行性，不参与 Memory Hub 服务排名。进程内异步入队不能作为已持久提交的依据；跨业务表统一事务需要额外验证。

## Google ADK-Go（框架参考）

| 来源 | 核查用途 |
| --- | --- |
| [仓库](https://github.com/google/adk-go) | Go Agent 框架定位、主许可和局部许可例外 |
| [Session 目录](https://github.com/google/adk-go/tree/main/session) | 会话、数据库与 compaction 实现入口 |
| [Database 目录](https://github.com/google/adk-go/tree/main/session/database) | 关系数据库会话存储及测试文件 |
| [Database Service 源码](https://github.com/google/adk-go/blob/main/session/database/service.go) | GORM 后端、Create/Get/AppendEvent、状态增量与事务；现有连接复用入口 |

核查重点：已存在关系数据库 Session Service。不能从连接复用或库内事务推导出本项目所有表已具备原子提交。

## Eino（框架参考）

| 来源 | 核查用途 |
| --- | --- |
| [仓库](https://github.com/cloudwego/eino) | Go 框架、ADK、工具与主许可 |
| [中断与恢复](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_hitl/) | CheckPoint ID、CheckPoint Store、中断恢复与跨进程持久化要求 |

核查重点：Agent 执行恢复。Checkpoint 不是本项目完整的会话/设备同步数据模型。

## LangGraph（框架参考）

| 来源 | 核查用途 |
| --- | --- |
| [仓库](https://github.com/langchain-ai/langgraph) | 有状态编排定位、核心库 MIT、Python 与 JS/TS 生态 |
| [Persistence](https://docs.langchain.com/oss/python/langgraph/persistence) | Thread、Checkpoint、状态快照和 PostgreSQL Checkpointer |

核查重点：执行状态持久化机制；核心库与相关托管/部署产品的开源及许可范围分别确认。

## Redis Agent Memory Server

| 来源 | 核查用途 |
| --- | --- |
| [当前仓库](https://github.com/redis/agent-memory-server) | 当前托管产品入口、V0 研究资料定位、仓库主许可 |
| [V0 README](https://github.com/redis/agent-memory-server/blob/main/V0/README.md) | 原 Python/FastAPI + Redis 开源服务器、工作记忆与长期记忆 |

核查重点：原开源服务的版本边界发生变化。当前 README 明确 V0 为研究资料，不能按当前受支持的生产发行版进行选型。

## 来源与判断的边界

项目能力来自上述官方资料。跨端增量同步契约、统一业务事务、CAS、fencing、候选优先级和 PoC 项目属于 Memory Hub 的需求及设计判断。

“未确认”表示所查资料没有证实满足指定业务语义，不代表已经穷尽全部源码或证明功能不存在。正式选型时需固定版本、复查许可与接口，并执行主报告列出的验证。
