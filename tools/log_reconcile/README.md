# log_reconcile

基于币安订单自动补全/校正决策日志中的平仓记录。

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

- **补全**: 为缺失平仓的开仓生成 decision_reconcile_*.json
- **校正**: 修正价格/数量偏差 >1% 的记录 (自动备份为 .bak)
- **隔离**: 多交易员数据独立处理

---
v1.0 | 2025-11-12
