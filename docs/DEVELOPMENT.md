# 工程设计与第一阶段任务

## 已确定的范围

- 独立部署的 Go 服务，首个调用方为 sub2api 后端。
- 首版支付资产为 USDT，网络为 TRON，代币标准为 TRC20。
- Go HTTP API、PostgreSQL 持久化、pgx 数据访问、SHKeeper 收款网关。
- 先完成一条可靠收款链路，再扩展币种、商户及自动退款。
- 设计、实现、代码审查和真实依赖验证按阶段推进；项目内提供 Zed 开发任务。

PostgreSQL 连接池及订单 Create/Get 已实现，内存仓储保留用于单元测试。以下网关、后台任务和入账契约仍为待实现设计。订单持久化不等于真实支付链路完成。

## 业务与资金边界

sub2api 负责登录身份、充值商品、价格和应增加的余额。它先建立一笔稳定的业务充值订单，再带该业务订单号调用支付服务。网络重试必须使用相同业务订单号。

已使用的业务订单号不复用；支付订单不通过硬删除来清理。重试返回原订单及其原始过期时间，不延长有效期。

支付服务保存支付订单、分配或获取收款信息、核验支付、记录事件并可靠地通知 sub2api。支付服务不直接修改 sub2api 数据库。

SHKeeper 负责收款网关部分。支付服务不保存钱包助记词或私钥。

付款确认之后，在同一个 PostgreSQL 事务中提交订单确认状态和待发送通知任务（outbox）。后台任务发送通知；sub2api 接收端按支付订单号幂等入账。只有接收端明确确认入账事务已提交，才标记履约完成；仅收到请求或排入队列的响应不算入账完成。

不能把本地数据库提交和远端 HTTP 调用当作同一个事务。发送超时可能意味着远端已经入账，重试仍然使用相同幂等标识。

## 金额与网络契约

| 数据 | 表示方式 | 用途 |
|---|---|---|
| 业务标价 | USD 整数分，如 `1000` | 表示 `10.00 USD` 的业务订单价格 |
| 链上应付金额 | USDT 最小单位整数，接口以十进制字符串传输 | 从网关报价换算并保存；不使用 float64 |
| 链上已付金额 | 通过核验的转账最小单位整数之和 | 支持同一订单分多次付款 |
| 平台入账额度 | sub2api 业务订单中的价格与额度快照 | 避免付款后价格规则变化导致错误入账 |

USDT / TRON 使用 6 位小数，即 `1 USDT = 1000000` 个最小单位；参考 [TRON 精度说明](https://developers.tron.network/docs/getting-testnet-tokens-on-tron)。USD 标价不能直接当作应付 USDT 数量。SHKeeper 创建发票的标价字段和返回的加密货币金额应分别处理，见 [SHKeeper API](https://shkeeper.io/api/)。

主网 USDT 的合约地址以 [Tether 官方协议列表](https://tether.to/en/supported-protocols/) 为准。主网和测试网使用独立配置与数据，不能只根据代币名称 `USDT` 判断付款资产。

每个付款快照最终必须明确网络环境、代币合约、代币精度、收款地址、应付数量及报价有效期。区分“网关按其规则确认”与“独立通过 TRON 节点再次核验”，两者验收证据不同。

## 模块划分

| 模块 | 职责 |
|---|---|
| HTTP 接口 | 商户鉴权、请求解析、错误响应、请求上下文 |
| 订单业务 | 参数校验、幂等创建、订单状态转换 |
| PostgreSQL 仓储 | 唯一约束、原子更新、事务、查询 |
| 支付网关适配器 | 创建与查询 SHKeeper 发票、核验回调来源 |
| 回调接收与核验 | 保留原始字节、验证签名、核对订单和付款结果 |
| 后台任务 | 补查、超时任务、通知重试及人工处理入口 |

首版后台任务可从 PostgreSQL 领取持久化任务，使用任务租约和重试时间控制并发，不必先引入 Redis 或消息队列。

## 状态与重复执行

支付状态与履约状态独立：

```text
支付：PENDING → CONFIRMED
           → EXPIRED / CANCELLED / MANUAL_REVIEW

履约：NOT_READY → PENDING → PROCESSING → COMPLETED
                                    → RETRY / MANUAL_REVIEW
```

`CONFIRMED` 表示已按约定策略确认付款；`COMPLETED` 表示 sub2api 已确认入账。付款后入账失败，只重试履约，不重新发起收款。

过期后付款、多付、资产或金额异常先保留证据并进入人工处理。不能让重复回调绕过状态和幂等检查。回调中的验签时间窗口用于消息校验，不能拿订单过期时间替代。

## 第一阶段：持久化订单与幂等创建

目标：实现 Go 进程和数据库重启后仍可查询同一订单，并且多个进程同时处理重试也只能创建一单。

### 1. 先写数据库迁移

新增 `migrations/000001_payment_orders.up.sql`，定义 `payment_orders`。首版字段如下：

| 字段 | PostgreSQL 类型 | 约束与来源 |
|---|---|---|
| id | TEXT | 主键；由服务端生成，沿用现有 `pay_...` 编号也可以 |
| merchant_id | TEXT | NOT NULL；从商户密钥对应的服务端配置取得，首个商户为 hajimi |
| merchant_order_no | VARCHAR(64) | NOT NULL；调用方业务订单号 |
| user_id | BIGINT | NOT NULL、正数；sub2api 传入的外部用户标识，不外键关联另一个服务的用户表 |
| amount_cents | BIGINT | NOT NULL、正数；USD 标价，保持精确整数 |
| currency | VARCHAR(3) | NOT NULL；本阶段限制为 USD |
| asset | TEXT | NOT NULL；本阶段固定 USDT，由服务端填入 |
| network | TEXT | NOT NULL；本阶段固定 TRON，由服务端填入 |
| payment_status | TEXT | NOT NULL；初始 PENDING，限制为上文列出的支付状态 |
| fulfillment_status | TEXT | NOT NULL；初始 NOT_READY，限制为上文列出的履约状态 |
| expires_at | TIMESTAMPTZ | NOT NULL；由服务端超时策略决定 |
| confirmed_at | TIMESTAMPTZ | 可空；付款确认时写入 |
| created_at | TIMESTAMPTZ | NOT NULL；默认当前时间 |
| updated_at | TIMESTAMPTZ | NOT NULL；默认当前时间，每次状态变化显式更新 |
| version | BIGINT | NOT NULL、正数；默认 1，用于后续条件更新 |

必须添加命名唯一约束：

```sql
CONSTRAINT payment_orders_merchant_order_unique
    UNIQUE (merchant_id, merchant_order_no)
```

商户、订单号、用户、金额和币种等参与幂等比较的字段均必须非空。PostgreSQL 的复合唯一约束用于跨进程防重，参考 [约束文档](https://www.postgresql.org/docs/current/ddl-constraints.html#DDL-CONSTRAINTS-UNIQUE-CONSTRAINTS)。

迁移通过版本管理工具应用到本服务独立的开发数据库，不在应用每次启动时自动建表。首阶段不连接现有 sub2api 生产数据库。迁移在真实 PostgreSQL 上执行成功后才算完成，SQL 文件存在不代表已验证。

### 2. 调整仓储契约

现有 `OrderStore` 的两个方法加上请求上下文：

```go
type OrderStore interface {
    Create(ctx context.Context, req order.CreateRequest) (order.Order, bool, error)
    Get(ctx context.Context, id string) (order.Order, error)
}
```

HTTP 层传 `r.Context()`。首版一个服务实例绑定一个商户 API 密钥，仓储在启动时绑定服务端配置 PAYMENT_MERCHANT_ID；不从请求 JSON 接收 merchant_id。查询 SQL 同时限定绑定的商户 ID 和支付订单 ID。通过为不同商户构造不同的仓储实例验证隔离；当前 HTTP 配置不支持同时接收多个商户密钥。

本阶段保留现有 Order 请求/响应字段，SQL 把 payment_status 映射到原有 status 响应字段。资产、网络及过期时间由服务端写入，履约字段采用数据库初始状态；后续网关阶段再扩展响应，避免为持久化改造引入不必要的接口变化。时间统一输出 UTC 的 RFC3339 格式。

新增 PostgreSQL 实现文件 `internal/order/postgres.go`。连接池在启动时创建、检查连通性并在退出时关闭；生产路径要求数据库配置有效，连接失败时不能自动退回内存存储。

### 3. 实现幂等插入

采用数据库唯一约束作为最终仲裁：

1. 校验输入，转换为整数金额，生成候选订单号。
2. `INSERT ... ON CONFLICT ON CONSTRAINT payment_orders_merchant_order_unique DO NOTHING RETURNING ...`。
3. 插入返回订单时，返回 `replayed=false`。
4. 没有插入时，发起新的查询，按商户及业务订单号读取已提交订单。
5. 比较用户、金额、币种、资产和网络等不可变业务字段；一致则返回原订单与 `replayed=true`，不一致则返回冲突。

不按“先 SELECT 不存在，再 INSERT”来保证唯一性。读不到冲突订单、数据库超时或提交结果不确定时，不声称创建失败后可以另开新单；调用方仍按同一业务订单号重试。

显式事务中每个操作均传上下文，并确保每条退出路径提交或回滚。pgx 事务不会仅因开始事务时的上下文取消而自动回滚，见 [pgxpool 文档](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool#Pool.BeginTx)。

### 4. 完成验收

- 正常创建可查回相同字段，金额是精确整数。
- 重启 Go 服务后，查询和重试仍得到原订单。
- PostgreSQL 重启后，已提交订单仍存在。
- 使用多个独立数据库连接并发提交同一业务订单，最终仅有一行，且只有一个调用报告首次创建。
- 同一业务订单号修改用户、金额或其他不可变字段返回冲突，原订单不变。
- 不同商户使用同一业务订单号时分别创建订单，不能互相读取订单；每次查询按当前认证商户限定。
- 数据库不可达时返回服务不可用，不返回“订单不存在”，不退回内存。
- 真实 PostgreSQL 集成测试通过后再进入网关阶段；内存仓储测试不能替代它。

## 后续里程碑

1. 支付尝试表和报价快照；确定 SHKeeper 已部署版本、可用 USDT 钱包、回调协议及查询结果的状态含义。
2. 创建发票结果持久化，处理“网关已建单但本地写入或网络响应失败”。沿用稳定 external_id 查询恢复。
3. 回调验签和原始事件落库；按约定确认策略核对网络、合约、地址、金额和最终状态。若做独立链上核验，补充 TRON 节点读取和转账事件去重实现。
4. 订单确认与 outbox 同事务写入；sub2api 接收端按业务订单事务入账、防重复，并返回可核验的入账结果。
5. 补查、通知重试、租约回收、人工处理和审计；测试重复、乱序、断电、超时及中途崩溃后的恢复。
6. Linux ARM64 构建、配置与密钥管理、日志和指标、备份恢复、预发布环境验收后部署。

每个里程碑分别记录代码完成、本地测试、真实依赖集成、预发布验证和线上验收，不能互相替代。
