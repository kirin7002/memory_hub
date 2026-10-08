# 参考资料（Reference）

调研日期：2026-10-01。来源为官方仓库、官方文档和原始评测项目。

## 项目设计摘要

- [Mem0：可嵌入的长期记忆组件](mem0.md)
- [Letta：记忆与 Agent 状态结合](letta.md)
- [Memobase：画像与事件分开](memobase.md)
- [Graphiti：事实需要时间和来源](graphiti.md)
- [LangGraph：对话状态与长期数据分别持久化](langgraph.md)
- [MemOS：按权限组合记忆空间](memos.md)
- [Hindsight：召回与反思分离](hindsight.md)

## 官方来源与用途

文中项目能力均链接至相关原始来源；以下为便于复查的资料索引。除资料所描述的项目能力外，三层业务协议、同步算法、数据模型与验收目标均为本文设计。

| 主题 | 官方来源 |
| --- | --- |
| Mem0 开源范围 | [仓库](https://github.com/mem0ai/mem0)、[OSS 概览](https://docs.mem0.ai/open-source/overview) |
| Mem0 当前算法、接口和图记忆变化 | [OSS 迁移说明](https://docs.mem0.ai/migration/oss-v2-to-v3) |
| Mem0 配置与抽取 | [pgvector](https://docs.mem0.ai/components/vectordbs/dbs/pgvector)、[自定义指令](https://docs.mem0.ai/open-source/features/custom-instructions) |
| Letta 当前与历史源码 | [旧仓库说明](https://github.com/letta-ai/letta)、[Letta Code](https://github.com/letta-ai/letta-code) |
| Letta 记忆 | [当前记忆模型](https://docs.letta.com/agent-sdk/memory)、[V1 状态模型，仅作历史参考](https://docs.letta.com/v1-sdk/concepts/stateful-agents) |
| Memobase | [仓库](https://github.com/memodb-io/memobase)、[部署示例](https://github.com/memodb-io/memobase/blob/main/src/server/docker-compose.yml) |
| Graphiti 与 Zep 边界 | [Graphiti](https://github.com/getzep/graphiti) |
| LangGraph | [仓库](https://github.com/langchain-ai/langgraph)、[持久化](https://docs.langchain.com/oss/python/langgraph/persistence)、[记忆概念](https://docs.langchain.com/oss/python/concepts/memory) |
| MemOS | [仓库](https://github.com/MemTensor/MemOS) |
| Hindsight | [仓库](https://github.com/vectorize-io/hindsight)、[存储架构](https://hindsight.vectorize.io/developer/storage) |
| 数据库事务 | [PostgreSQL 隔离级别](https://www.postgresql.org/docs/current/transaction-iso.html)、[锁](https://www.postgresql.org/docs/current/explicit-locking.html) |
| 向量与缓存基础组件 | [pgvector](https://github.com/pgvector/pgvector)、[Valkey](https://github.com/valkey-io/valkey) |
| 公开记忆评测 | [LoCoMo](https://github.com/snap-research/locomo)、[LongMemEval](https://github.com/xiaowu0162/LongMemEval) |

## 资料范围

本目录保存参考链接及归纳摘要，未复制第三方完整仓库或网页。文中初步选型基于所列资料，正式采用时需固定版本或 commit SHA，并通过业务 PoC 验证。

## 云侧与端侧一期设计参考

- [Go 云侧服务官方资料](go-cloud-service.md)：pgx、sqlc、事务隔离、后台队列、Go HTTP、迁移与云数据库连接。
- [Go 云侧详细设计](../phase1-go-cloud-service-design.md)：当前重点，包含 Go 接口、DDL、端 Agent 上下文获取、离线导入、同步和故障恢复设计。

- [一期云侧与端侧总览](../phase1-memory-system-design.md)：当前实施边界与文档入口。
- [端侧简要方案与后续 Demo](../phase1-edge-demo-design.md)：端侧契约与后续简单 Demo，当前不依赖端侧实现。

## 云侧开源服务与 Go 框架补充调研

- [云侧开源项目 Reference](cloud-open-source.md)：会话、上下文、执行恢复的官方仓库、文档及部分存储源码。
- [云侧开源仓库对比与复用建议](../cloud-open-source-comparison.md)：一期服务候选优先核查 Acontext；tRPC-Agent-Go 等仅作为 Agent 框架参考，包含同步一致性 PoC。

## 会话时间线与时钟依据

- [时间线 Reference](timeline-ordering.md)：RFC 5905、NTP 误差模型与 Lamport 因果顺序。
- [实际时间线设计](../phase1-timeline-ordering-design.md)：本项目的读模型与保证边界，尚未实现或实测。
