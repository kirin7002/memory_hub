# 会话时间线 Reference

核查日期：2026-10-01。对应 [时间线设计](../phase1-timeline-ordering-design.md) 与 [云侧详细设计](../phase1-go-cloud-service-design.md)。

| 官方来源 | 核查用途 | 与 MemoryHub 的边界 |
| --- | --- | --- |
| [RFC 5905：NTPv4](https://www.rfc-editor.org/rfc/rfc5905.html) | 四时间戳、偏移/网络延迟估计、时钟质量与授时模型 | 应用时间探测借鉴测量方法，不是完整 NTP 实现，不保证任何网络下的固定精度 |
| [NTP 授时与误差说明](https://www.ntp.org/reflib/exec/) | 网络路径不对称、参考时钟、时钟漂移与正确性区间 | 应保留误差及假设，不能把一次服务端时间响应当成绝对精确设备时间 |
| [Lamport：Time, Clocks and the Ordering of Events in a Distributed System](https://www.microsoft.com/en-us/research/publication/time-clocks-ordering-events-distributed-system/) | 因果部分序和与其一致的逻辑总序 | 确定性逻辑排列不证明独立离线事件的物理先后 |

event_time/commit_time 分离、端侧单调时间与流序号、DAG 时间线、固定水位分页、晚到刷新和摘要失效属于 MemoryHub 应用设计，尚未实现或实测。

不保存第三方文档全文。具体设备漂移界、云端参考误差、时间探测的测量开销与最终精度，需要实现后的设备和网络测试支持。
