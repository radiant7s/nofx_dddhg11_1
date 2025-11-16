# log_reconcile

基于币安订单自动补全/校正决策日志中的平仓记录（包含“部分平仓”）。

## 使用方法

```powershell
先扫描符号（必须先有符号，拉单才有目标）
go run ./tools/log_reconcile -action scan-symbols
按交易员从配置库拉单（新增了可见日志）
# 单一Binance账户（或只有一条binance记录）:
go run ./tools/log_reconcile -action fetch-orders-db -config_db config.db -user_id default -base fapi

# 如存在多个Binance账户，指定 exchange_id（例如 'binance'）:
go run ./tools/log_reconcile -action fetch-orders-db -config_db config.db -user_id default -base fapi -exchange_id binance

对账
go run ./tools/log_reconcile -action reconcile
```

## 功能

- **校正**: 修正价格/数量偏差 >1% 的记录 (自动备份为 .bak)
- **隔离**: 多交易员数据独立处理


- 开仓匹配：仅匹配非 reduceOnly/closePosition 且 `FILLED` 的订单。
- 平仓匹配：仅匹配 `close_long`/`close_short` 且 `FILLED` 的订单。
- 部分平仓（partial_close）：匹配 `reduceOnly=true` 的订单，接受以下状态：
	- `FILLED`；
	- `PARTIALLY_FILLED` 或 `CANCELED`，且已成交数量 `executedQty > 0`。

---
v1.0 | 2025-11-12
v1.1 | 2025-11-16 新增 `fetch-orders-db`，支持从 config.db 按交易员读取密钥
