# LangGraph：对话状态与长期数据分别持久化

调研日期：2026-10-01。以下为依据官方资料整理的设计摘要。

Checkpointer 的 thread scope 与 Store 的跨线程 scope，很适合支撑“当前任务状态”和“跨会话记忆”的边界。端侧同步的对象应是公共业务状态；运行时 Checkpoint 可保留在云端，并通过引用接续。[持久化文档](https://docs.langchain.com/oss/python/langgraph/persistence)

[完整调研与选型](../memory-system-design.md) · [参考资料索引](README.md)
