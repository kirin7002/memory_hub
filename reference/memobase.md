# Memobase：画像与事件分开

调研日期：2026-10-01。以下为依据官方资料整理的设计摘要。

稳定画像适合启动时读取，经历和时间线适合按问题检索。其缓冲与 flush 机制有利于把记忆加工移出实时响应链路；但跨端刚产生的消息不能只等缓冲区处理后才可见。[官方仓库说明](https://github.com/memodb-io/memobase)

[完整调研与选型](../memory-system-design.md) · [参考资料索引](README.md)
