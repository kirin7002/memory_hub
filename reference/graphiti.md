# Graphiti：事实需要时间和来源

调研日期：2026-10-01。以下为依据官方资料整理的设计摘要。

同一个关系在不同时间可能有不同值，应能回答“现在是什么”和“当时是什么”。本方案即使首期不用图数据库，也保留 `valid_from/valid_to` 和源事件引用，为以后引入时序图提供输入。[Graphiti 架构](https://github.com/getzep/graphiti)

[完整调研与选型](../memory-system-design.md) · [参考资料索引](README.md)
