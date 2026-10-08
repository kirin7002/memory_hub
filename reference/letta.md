# Letta：记忆与 Agent 状态结合

调研日期：2026-10-01。以下为依据官方资料整理的设计摘要。

旧 MemGPT/V1 方案中的核心记忆块、历史消息和归档记忆，是三层划分的重要参考。旧 V1 文档仍可用于理解这些概念，但不应据此设计新项目的当前接口。[V1 状态模型](https://docs.letta.com/v1-sdk/concepts/stateful-agents)

当前 Letta 文档采用 Git 支持的 MemFS：`system/` 下内容进入每轮提示，其他记忆按需读取。其托管共享记忆仓库属于云服务能力，自托管可以使用自己的 Git remote。对消费级多端产品，Git 记忆共享仍需搭配业务事件、权限和任务并发控制。[当前记忆文档](https://docs.letta.com/agent-sdk/memory)

[完整调研与选型](../memory-system-design.md) · [参考资料索引](README.md)
