# Go 云侧一期设计：官方参考资料

访问日期：2026-10-01。对应设计文档：[Go 云侧详细设计](../phase1-go-cloud-service-design.md)。一期整体边界见 [云侧与端侧总览](../phase1-memory-system-design.md)，端侧后续方案见 [端侧简要设计](../phase1-edge-demo-design.md)。

| 资料 | 用于核对的能力 | 与本方案的边界 |
| --- | --- | --- |
| [pgxpool](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool) | Go PostgreSQL 连接池、事务、池配置、启动连通性检查 | 不替代业务幂等与状态版本 |
| [sqlc transactions](https://docs.sqlc.dev/en/latest/howto/transactions.html) | 生成查询代码通过 WithTx 绑定事务 | 可选工程工具；事务算法由业务层负责 |
| [PostgreSQL 事务隔离](https://www.postgresql.org/docs/current/transaction-iso.html) | 同一事务快照、隔离级别、sequence 边界 | 用户提交水位采用本方案设计的事务计数器 |
| [PostgreSQL SELECT](https://www.postgresql.org/docs/current/sql-select.html) | 行锁、SKIP LOCKED | SKIP LOCKED 只用于后台队列领取 |
| [MySQL Locking Reads](https://dev.mysql.com/doc/refman/8.4/en/innodb-locking-reads.html) | InnoDB 锁定读取、计数器、SKIP LOCKED | MySQL 是替代选项，本文 DDL 主实现为 PostgreSQL |
| [golang-migrate](https://github.com/golang-migrate/migrate) | Go / CLI 数据库迁移 | 迁移命令与 API 启动分开 |
| [PostgreSQL NOTIFY](https://www.postgresql.org/docs/current/sql-notify.html) | 可选变化通知、提交后投递 | 不替代持久化同步变化流 |
| [Go net/http](https://pkg.go.dev/net/http) | HTTP Server、超时、流式响应、优雅关闭 | SSE 仍需要应用层补拉与游标 |
| [阿里云 RDS Supabase](https://help.aliyun.com/zh/rds/apsaradb-rds-for-postgresql/supabase/) | PostgreSQL 底座、连接、计费 | 购买规格与资格以实际订单为准 |
| [CloudBase PostgreSQL 连接](https://docs.cloudbase.net/database/postgresql/connecting-to-postgresql) | PostgreSQL 协议直连、事务、复杂 SQL | 空闲缩容、轮询成本与代理限制需要实测 |

本文为来源摘要，未复制网页全文。会话归属、Context Bundle、幂等回执、水位、Run fencing、同步协议和验收指标属于 MemoryHub 应用设计，不能当作上述组件的原生完整功能。

端侧 Agent 接入修订：Context Bundle/缓存元数据、设备 Run、离线执行记录导入与重校验详见云侧第 6.7、8.5、9.5 节。这些是 MemoryHub 的应用协议，不是 PostgreSQL 或 Go 组件自带的功能；本次仅修订设计，没有实现/验证真实车端模型。

时间线修订：提交序号仅用于同步/审计；对话展示采用校准事件时间与因果读模型。详见 [时间线设计](../phase1-timeline-ordering-design.md) 和 [时间线 Reference](timeline-ordering.md)，不声称完全离线时可获得绝对物理顺序。
