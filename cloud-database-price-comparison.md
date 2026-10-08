# 国内云数据库选型与 Demo 价格对比

调研日期：2026-10-01。用途：MemoryHub 一期会话、上下文、多端同步 Demo。

## 结论

个人 Demo 建议继续使用 PostgreSQL，优先查看阿里云 RDS Supabase 的 **227.99 元/年起**活动，或者腾讯 CloudBase PostgreSQL 的免费体验与 **19.9 元/月**个人套餐。前者底座是 RDS PostgreSQL，可以使用标准数据库连接；后者提供 PostgreSQL 协议直连，但按资源点计量，需要控制持续运行的计算用量。

若仅追求更低的公开活动起价、愿意将原方案的数据类型与 SQL 适配为 MySQL，可查看阿里云 RDS MySQL 的 **99 元/年起**活动。

这些是不同规格、不同计费方式的产品入口，不能据此得出同规格性能或价格排名。活动资格、实际规格、可选地域、磁盘与额外费用需要在购买页核对；本调研未登录账户核验权益，未创建付费资源。

## 国内厂商与公开价格

金额均为人民币。年付折算仅用于理解费用，不能按折算月价购买。

| 厂商 / 产品 | 引擎与配置口径 | 官方公开价格 | 折算月费 | 说明与来源 |
| --- | --- | --- | --- | --- |
| 阿里云 RDS MySQL 活动 | MySQL；99 计划的指定配置 | **99 元/年起** | 约 8.25 元 | 新老同享；活动期间新购、续费同价；需核对选定规格与限购资格。[官方活动](https://cn.aliyun.com/activity/database/bestoffers?from_alibabacloud=) |
| 阿里云 RDS Supabase 活动 | PostgreSQL 底座；公开静态页面未完整展示起价对应配置 | **227.99 元/年起** | 约 19 元 | 页面标注新老同享、续费同价；购买页确认实际实例与存储。[官方活动](https://cn.aliyun.com/activity/database/bestoffers?from_alibabacloud=) |
| 腾讯云 CloudBase PostgreSQL 免费体验 | 每月 3,000 资源点；单账户一个免费环境 | **0 元/月，有效期 6 个月** | 0 元 | 有额度限制，不是无限免费；免费版暂不支持资源包与按量付费。[产品与价格](https://cloud.tencent.com/product/tcbs) |
| 腾讯云 CloudBase PostgreSQL 个人版 | 每月 40,000 资源点；含数据库、函数、存储等能力 | **19.9 元/月** | 19.9 元 | 套餐基础费；并非固定核数数据库的无限用量总价。[产品与价格](https://cloud.tencent.com/product/tcbs) |
| 腾讯云 TencentDB MySQL | 经济型 1 核 2 GB；最大存储 50 GB | **实例规格费 99 元/月** | 99 元 + 磁盘 | 最大存储不等于赠送存储；实例最小计费单元为规格加硬盘。[官方定价](https://buy.cloud.tencent.com/price/cdb) |
| 华为云 RDS PostgreSQL | 通用型单机，2 核 4 GB；卡片推荐 40 GB SSD | **页面参考 196 元/月** | 196 元 + 其他计费项 | 页面明确说明“仅为实例规格参考价”，不能当作含磁盘总价。[官方产品页](https://www.huaweicloud.com/product/pg.html) |
| 华为云 RDS MySQL | 通用型单节点，2 核 4 GB；卡片推荐 40 GB SSD | **页面参考 196 元/月起** | 以结算为准 | 公开页面未清楚拆分该起价是否含磁盘，需核对完整订单。[官方产品页](https://www.huaweicloud.com/product/mysql.html) |
| 阿里云 / 腾讯云常规 RDS PostgreSQL | 独立托管 PostgreSQL；按地域与规格选配 | **需在配置页询价** | — | 不将 Supabase 或 CloudBase 套餐价当作常规 RDS 同规格报价。[阿里云](https://www.aliyun.com/product/rds/postgresql)、[腾讯云](https://cloud.tencent.com/product/postgres) |
| 火山引擎 RDS | MySQL、PostgreSQL | **本次未取得可核验的小规格总报价** | — | 支持两种引擎；计费示例不是个人 Demo 的低配报价，因此未用于价格排名。[MySQL 文档](https://www.volcengine.com/docs/6313?lang=zh)、[PostgreSQL 计费](https://www.volcengine.com/docs/6438/79224) |

阿里云 99 计划活动页标注截止日期为 2027-03-31。RDS Supabase 起价对应的 CPU、内存、存储及可选地域没有在本次获取的静态页面中完整呈现，因此没有采用社区文章中的规格作为已验证事实。

华为云价格来自官方产品卡片的公开页面配置数据，且页面提示最终金额以支付页为准。其 MySQL 与 PostgreSQL 的显示价格相同，并不证明其他地域、存储或高可用架构的总价相同。

## 两种低成本 PostgreSQL 产品的适用性

### 阿里云 RDS Supabase

官方文档说明：Supabase 模块免费，收取 RDS PostgreSQL 实例规格与存储费用；可以找到项目关联的 RDS 实例，设置白名单并连接 PostgreSQL 数据库。因此，自有后端可延续标准 PostgreSQL 驱动与事务实现，不必改用 Supabase SDK。[功能、计费与连接说明](https://help.aliyun.com/zh/rds/apsaradb-rds-for-postgresql/supabase/)

建议先核对 227.99 元/年起活动订单的配置是否满足需要，尤其是存储容量、备份、连接数和可选地域。不要把年付起价理解为所有配置均为这个价格，也不要将活动期间续费同价理解为永久承诺。

### 腾讯 CloudBase PostgreSQL

官方文档明确支持 SDK、HTTP API 和 PostgreSQL 协议客户端。自有后端可以直连，使用事务、复杂 SQL、批量写入与连接池，适合继续实现消息、上下文和 Outbox 的原子写入。[数据库连接方式](https://docs.cloudbase.net/database/postgresql/connecting-to-postgresql)

其产品页公开计量口径为：1,000 资源点对应 1 元，PostgreSQL CPU 为 0.342 元/核·小时，数据库容量为 0.0005 元/GB·小时；其他后端资源也会消耗点数。个人套餐 40,000 点对应约 40 元资源消耗额度，不能把 19.9 元套餐费理解为无限计算资源。[资源计量说明](https://cloud.tencent.com/product/tcbs)

作为纯计算示例，若按上述单价持续计量 1 核、每天 24 小时、30 天，CPU 消耗约 246.24 元等值资源点，未计数据库容量和其他服务。这不是具体实例报价，实际按产品的 CPU 计量规则与休眠情况结算。

MemoryHub 的 Outbox Worker 应验证空闲休眠与唤醒行为。持续轮询、长期连接是否妨碍休眠，需要用实际小规模试运行确认；不能假设数据库必然缩容到零。国内 Demo 可查看上海地域。

## 对 MemoryHub Demo 的建议

1. **预计运行数月、希望费用可预期：**先核对阿里云 RDS Supabase 年付活动；实际配置、存储和连接能力合适时作为优先候选。
2. **先免费验证功能：**腾讯 CloudBase PostgreSQL 免费体验可作为起点。确认账户资格、资源额度、服务端连接及空闲资源消耗，再决定是否升级个人版。
3. **更熟悉 MySQL，主要验证多端同步：**阿里云 RDS MySQL 活动是公开起价较低的选择。一期的消息日志、版本控制、Outbox 和事务可以用 MySQL 实现，但应调整 PostgreSQL 专属 JSONB 类型及相关 SQL。
4. **需要独立固定规格数据库：**选择普通 RDS PostgreSQL；按同一地域、相同节点架构、2 核 4 GB、相同磁盘与购买时长取得完整订单报价，再比较阿里、腾讯、华为和火山。

Demo 仍采用：手机 / 车端 → 自有 MemoryHub API → PostgreSQL；API 与 Worker 负责一致性和同步，端侧 SQLite 保存缓存与待发送事件。数据库管理员连接串放在服务端。

以上费用只覆盖数据库或所述后端套餐。自有 API 服务部署、对象存储、额外公网流量与模型调用费用应单独预算。

## References

以下均为厂商官方来源，访问日期 2026-10-01。

- [阿里云数据库上云优选：RDS MySQL 99 计划、RDS Supabase 活动价](https://cn.aliyun.com/activity/database/bestoffers?from_alibabacloud=)
- [阿里云 RDS Supabase：功能、计费、数据库连接](https://help.aliyun.com/zh/rds/apsaradb-rds-for-postgresql/supabase/)
- [阿里云 RDS PostgreSQL 产品](https://www.aliyun.com/product/rds/postgresql)
- [腾讯 CloudBase for Supabase：套餐、额度、计量与地域](https://cloud.tencent.com/product/tcbs)
- [腾讯 CloudBase PostgreSQL：数据库连接方式](https://docs.cloudbase.net/database/postgresql/connecting-to-postgresql)
- [腾讯云 MySQL 官方价格](https://buy.cloud.tencent.com/price/cdb)
- [腾讯云 PostgreSQL 产品](https://cloud.tencent.com/product/postgres)
- [华为云 RDS PostgreSQL 产品与规格参考价格](https://www.huaweicloud.com/product/pg.html)
- [华为云 RDS MySQL 产品与规格参考价格](https://www.huaweicloud.com/product/mysql.html)
- [火山引擎 RDS MySQL 文档](https://www.volcengine.com/docs/6313?lang=zh)
- [火山引擎 RDS PostgreSQL 计费项与价格](https://www.volcengine.com/docs/6438/79224)
