# MemOS：按权限组合记忆空间

调研日期：2026-10-01。以下为依据官方资料整理的设计摘要。

Memory Cube 的隔离、共享和组合思路，可用于用户、车辆、家庭、项目和 Agent 的不同记忆空间。自托管服务、本地插件和云 API 需要分别评估；本地插件的 SQLite 部署不能直接代表自托管服务的依赖规模。[MemOS 官方说明](https://github.com/MemTensor/MemOS)

[完整调研与选型](../memory-system-design.md) · [参考资料索引](README.md)
