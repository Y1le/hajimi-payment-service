# Hajimi Payment Service

面向 sub2api 的独立 Go 支付服务，首版计划接入 USDT / TRON（TRC20）。

这是独立的 Go 模块，可以单独启动。它与 sub2api 通过 HTTP 接口对接，不依赖 sub2api 的内部 Go 包。

目标是为 sub2api 提供独立的 USDT / TRON（TRC20）收款、付款核验及充值通知服务。技术栈采用 Go、PostgreSQL（pgx）和 SHKeeper。

**当前工程状态：** 已实现开发用订单创建、查询及进程内幂等。存储仍为内存，重启后数据丢失；PostgreSQL、SHKeeper、付款核验和 sub2api 入账通知尚未实现。当前版本不具备实际收款能力。

开发按 [工程设计与第一阶段任务](docs/DEVELOPMENT.md) 推进，每个阶段用明确的验收结果确认完成。

## 使用 Zed

克隆仓库后，把仓库目录作为项目根目录打开：

```powershell
git clone https://github.com/Y1le/hajimi-payment-service.git
zed hajimi-payment-service
```

项目内已配置 `.zed/tasks.json`。从命令面板执行 `task: spawn`，选择“支付服务：测试”“支付服务：静态检查”或“支付服务：编译检查”。任务配置与用法参考 [Zed 官方文档](https://zed.dev/docs/tasks)。

## 最终业务边界

```text
用户 → sub2api 后端（确认用户身份和充值价格）
     → 本支付服务（保存订单、调用 SHKeeper、核验付款、安排入账）
     → SHKeeper（提供收款信息、跟踪链上付款）

付款通过核验后：本支付服务 → 持久化充值通知任务 → sub2api 的幂等入账接口 → 用户余额
```

支付服务管理支付订单。sub2api 继续管理用户、API Key 和账户余额。

本版只有一个商户，即 sub2api 后端。`PAYMENT_API_KEY` 是两台服务之间使用的密钥，不能发送给浏览器或 App。

## 启动当前开发进程

要求 Go 1.22 或更新版本，当前无第三方依赖。在仓库根目录的 PowerShell 中运行：

```powershell
$env:PAYMENT_API_KEY = Read-Host '设置一个至少 32 字符的本地测试密钥'
go run ./cmd/server
```

默认监听 `127.0.0.1:5210`。可通过环境变量 `PAYMENT_ADDR` 指定其他监听地址。按 Ctrl+C 停止。

## 手动验证

保持启动窗口运行，在另一个 PowerShell 窗口运行下面的命令。输入与启动时相同的测试密钥：

```powershell
$paymentKey = Read-Host '输入启动时设置的同一测试密钥'
$paymentHeaders = @{ Authorization = "Bearer $paymentKey" }

Invoke-RestMethod -Uri 'http://127.0.0.1:5210/healthz'

$paymentBody = @{
    merchant_order_no = 'recharge_10001'
    user_id = 42
    amount = '10.00'
    currency = 'USD'
} | ConvertTo-Json

$createdOrder = Invoke-RestMethod -Method Post `
    -Uri 'http://127.0.0.1:5210/api/v1/orders' `
    -Headers $paymentHeaders -ContentType 'application/json' -Body $paymentBody
$createdOrder | ConvertTo-Json -Depth 4

Invoke-RestMethod `
    -Uri "http://127.0.0.1:5210/api/v1/orders/$($createdOrder.order.id)" `
    -Headers $paymentHeaders
```

再执行一次创建请求，应当得到相同的订单 ID，`replayed` 为 `true`。把金额改成 `20.00`，保持同一业务订单号，应得到 HTTP 409。

## 接口约定

| 接口 | 用途 | 认证 |
|---|---|---|
| `GET /healthz` | 检查进程是否能响应 | 无 |
| `POST /api/v1/orders` | 创建订单或返回已创建的同一订单 | 商户 Bearer 密钥 |
| `GET /api/v1/orders/{id}` | 查询支付服务订单 | 商户 Bearer 密钥 |

创建请求：

```json
{
  "merchant_order_no": "recharge_10001",
  "user_id": 42,
  "amount": "10.00",
  "currency": "USD"
}
```

| 字段 | 含义与限制 |
|---|---|
| `merchant_order_no` | sub2api 后端为一笔业务充值分配的稳定唯一编号；1–64 位字母、数字、下划线或连字符。网络重试必须沿用这个编号。 |
| `user_id` | 由 sub2api 后端从登录身份中取得的正整数用户 ID；后端不能直接信任浏览器传来的用户 ID。 |
| `amount` | 美元标价字符串，必须大于 0，最多两位小数；不接受浮点 JSON 数字或科学计数法。 |
| `currency` | 业务订单的标价币种，目前固定为 `USD`；与链上支付资产 `USDT` 分开。 |

响应中的 `order.amount_cents` 是美元整数分，例如 `1000` 表示 `10.00 USD`。金额不经过 `float64`。

首次创建返回 HTTP 201、`replayed: false`；相同业务订单号和相同用户、金额、币种的请求返回 HTTP 200、原订单及 `replayed: true`。同一业务订单号换用户或金额会得到 HTTP 409。`"10"` 与 `"10.00"` 视为同一金额。

所有订单接口仅供商户后端调用。向终端用户展示查询结果前，商户后端还须检查该订单的 `user_id` 属于当前登录用户。

请求不能指定 `status` 或把订单改成已付款。当前服务没有修改订单为已付款、充值余额或模拟收款的公开接口。

## 从哪里开始读

```text
cmd/server/main.go          读取配置、启动 HTTP 服务、退出时等待请求结束
internal/api/server.go     注册路由、验证商户密钥、解析请求、返回结果
internal/order/order.go    订单字段、金额校验、整数分转换
internal/order/memory.go   保存与查询订单、拦截并发重复创建
```

HTTP 路由使用 Go 标准库 `net/http`，参考 [ServeMux 官方文档](https://pkg.go.dev/net/http#ServeMux)。

## 当前订单实现

沿 `POST /api/v1/orders → createOrder → MemoryStore.Create` 读一遍代码，重点理解：

1. 为什么金额是字符串输入，却是整数分保存？
2. 为什么先查业务订单号、再保存订单这两步必须在同一把锁内？
3. 为什么重复请求修改了用户或金额时，必须报错？

下一阶段实现 PostgreSQL 存储：让订单和幂等记录在重启后仍然存在。数据库里需要给商户业务订单号设置唯一约束；进程内的锁不能替代数据库约束。具体字段、接口调整和验收条件见开发文档。

## 后续开发顺序

1. PostgreSQL 持久化与并发创建测试。
2. 接入 USDT / TRON（TRC20），保存 SHKeeper 收款信息，实现创建与查询发票；处理调用超时后结果不确定的情况。
3. 回调原文验签、主动核验付款、订单状态迁移。
4. 独立记录“已付款”和“已入账”，通过可靠任务及接收端幂等接口给 sub2api 入账。
5. 补查、失败重试，以及少付、多付、超时付款的处理。

入账接收端必须依据支付订单号防重，避免“余额已增加但 HTTP 响应丢失”导致重试加款两次。

## 检查

```powershell
go test ./...
go vet ./...
```

测试覆盖金额精度及溢出、64 个并发重复请求、重试参数冲突、接口认证、伪造付款字段和错误请求。

GitHub Actions 配置了 Windows / Linux 测试、静态检查、编译、Linux 并发竞争检测和 Linux ARM64 交叉编译。这些检查覆盖当前代码，不代表实际支付链路验收。

## 许可证

本项目采用 [MIT License](LICENSE)。
