# Hindsight：召回与反思分离

调研日期：2026-10-01。以下为依据官方资料整理的设计摘要。

把事实写入、信息召回和更深入的反思分成不同操作，适合为车端设置低延迟读取路径，为云端设置后台加工路径。它的 PostgreSQL 架构也便于首期控制存储依赖数量。[官方仓库](https://github.com/vectorize-io/hindsight)、[存储文档](https://hindsight.vectorize.io/developer/storage)

[完整调研与选型](../memory-system-design.md) · [参考资料索引](README.md)
