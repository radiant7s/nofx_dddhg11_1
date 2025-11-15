# log_reconcile

基于币安订单自动补全/校正决策日志中的平仓记录（包含“部分平仓”）。

## 使用方法

```powershell
# 1. 扫描交易对
go run ./tools/log_reconcile -action scan-symbols

# 2. 拉取订单 (需要币安 API 密钥)
go run ./tools/log_reconcile -action fetch-orders -api_key YOUR_KEY -secret_key YOUR_SECRET


# 3. 执行对账
go run ./tools/log_reconcile -action reconcile
```

## 主要参数

- -action: scan-symbols / fetch-orders / reconcile
- -api_key: 币安 API Key (仅步骤 2 需要)
- -secret_key: 币安 Secret Key (仅步骤 2 需要)
- -decision_dir: 日志目录 (默认 decision_logs)

## 功能

- **补全**: 为缺失平仓的开仓生成 decision_reconcile_*.json。
	- 若匹配到的订单是 reduceOnly 且非 closePosition，则自动生成为 `partial_close`；
	- 否则生成为 `close_long`/`close_short`，数量与成交量一致。
- **校正**: 修正价格/数量偏差 >1% 的记录 (自动备份为 .bak)
- **隔离**: 多交易员数据独立处理

## 匹配规则说明（简要）

- 开仓匹配：仅匹配非 reduceOnly/closePosition 且 `FILLED` 的订单。
- 平仓匹配：仅匹配 `close_long`/`close_short` 且 `FILLED` 的订单。
- 部分平仓（partial_close）：匹配 `reduceOnly=true` 的订单，接受以下状态：
	- `FILLED`；
	- `PARTIALLY_FILLED` 或 `CANCELED`，且已成交数量 `executedQty > 0`。

---
v1.0 | 2025-11-12
