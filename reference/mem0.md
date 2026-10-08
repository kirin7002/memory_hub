# Mem0：可嵌入的长期记忆组件

调研日期：2026-10-01。以下为依据官方资料整理的设计摘要。

通过 `add/search` 等接口连接现有应用，便于把记忆能力封装为独立服务。当前 OSS 迁移文档描述的是单次 ADD-only 抽取：信息变化时新事实与旧事实并存，检索负责排序；这不等于业务字段已经完成严格覆盖和失效处理。[官方迁移说明](https://docs.mem0.ai/migration/oss-v2-to-v3)

本方案借鉴其抽取和检索能力，同时增加自有的有效状态、来源及版本管理。pgvector 是其官方支持的后端之一。[pgvector 接入文档](https://docs.mem0.ai/components/vectordbs/dbs/pgvector)

[完整调研与选型](../memory-system-design.md) · [参考资料索引](README.md)
