package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DecisionRecordPart 仅解析需要的字段
type DecisionRecordPart struct {
	Timestamp    time.Time        `json:"timestamp"`
	DecisionJSON string           `json:"decision_json"`
	Decisions    []DecisionAction `json:"decisions"`
}

// DecisionAction from logger
type DecisionAction struct {
	Action    string    `json:"action"`
	Symbol    string    `json:"symbol"`
	Quantity  float64   `json:"quantity"`
	Leverage  int       `json:"leverage"`
	Price     float64   `json:"price"`
	OrderID   int64     `json:"order_id"`
	Timestamp time.Time `json:"timestamp"`
	Success   bool      `json:"success"`
	Error     string    `json:"error"`
}

// BinanceOrder 简化结构（含原始 JSON）
type BinanceOrder struct {
	AvgPrice         string `json:"avgPrice"`
	ClientOrderID    string `json:"clientOrderId"`
	CumBase          string `json:"cumBase"`
	ExecutedQty      string `json:"executedQty"`
	OrderID          int64  `json:"orderId"`
	OrigQty          string `json:"origQty"`
	OrigType         string `json:"origType"`
	Price            string `json:"price"`
	ReduceOnly       bool   `json:"reduceOnly"`
	Side             string `json:"side"`
	PositionSide     string `json:"positionSide"`
	Status           string `json:"status"`
	StopPrice        string `json:"stopPrice"`
	ClosePosition    bool   `json:"closePosition"`
	Symbol           string `json:"symbol"`
	Pair             string `json:"pair"`
	Time             int64  `json:"time"`
	TimeInForce      string `json:"timeInForce"`
	Type             string `json:"type"`
	ActivatePrice    string `json:"activatePrice"`
	PriceRate        string `json:"priceRate"`
	UpdateTime       int64  `json:"updateTime"`
	WorkingType      string `json:"workingType"`
	PriceMatch       string `json:"priceMatch"`
	SelfTradePrevent string `json:"selfTradePreventionMode"`
}

// 常量
const (
	defaultInterval = 3 * time.Second
	// 订单与决策时间匹配窗口（毫秒）
	timeToleranceMs = 30 * 60 * 1000
	createSchema    = `CREATE TABLE IF NOT EXISTS symbols(
	trader_id TEXT,
	symbol TEXT,
	first_seen INTEGER,
	PRIMARY KEY(trader_id, symbol)
);
CREATE TABLE IF NOT EXISTS orders(
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	trader_id TEXT,
	symbol TEXT,
	order_id INTEGER,
	side TEXT,
	position_side TEXT,
	status TEXT,
	avg_price REAL,
	executed_qty REAL,
	orig_qty REAL,
	reduce_only INTEGER,
	close_position INTEGER,
	type TEXT,
	time INTEGER,
	update_time INTEGER,
	raw_json TEXT,
	UNIQUE(trader_id, symbol, order_id)
);
CREATE TABLE IF NOT EXISTS reconcile_state(
	trader_id TEXT,
	symbol TEXT,
	last_order_id INTEGER,
	last_fetch_time INTEGER,
	PRIMARY KEY(trader_id, symbol)
);`
)

func main() {
	var action string
	var decisionDir string
	var dbPath string
	var apiKey string
	var secretKey string
	var intervalSec int
	var base string
	var configDBPath string
	var userID string
	var exchangeID string

	flag.StringVar(&action, "action", "scan-symbols", "scan-symbols|fetch-orders|fetch-orders-db|reconcile|partial-close-reconcile")
	flag.StringVar(&decisionDir, "decision_dir", "decision_logs", "决策日志根目录")
	flag.StringVar(&dbPath, "db", filepath.Join("tools", "log_reconcile", "reconcile.db"), "数据库文件路径")
	flag.StringVar(&apiKey, "api_key", "", "币安 API Key")
	flag.StringVar(&secretKey, "secret_key", "", "币安 Secret Key")
	flag.IntVar(&intervalSec, "interval_sec", 3, "拉取间隔秒")
	flag.StringVar(&base, "base", "fapi", "fapi 或 dapi")
	flag.StringVar(&configDBPath, "config_db", "config.db", "配置数据库文件路径(读取交易员与密钥)")
	flag.StringVar(&userID, "user_id", "default", "配置库中的用户ID")
	flag.StringVar(&exchangeID, "exchange_id", "", "回退模式下使用的交易所ID（如: binance），当没有交易员绑定时生效")
	flag.Parse()

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		log.Fatalf("创建目录失败: %v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	// 设置 SQLite 参数优化并发写入
	_, _ = db.Exec("PRAGMA journal_mode=WAL")
	_, _ = db.Exec("PRAGMA busy_timeout=5000")
	_, _ = db.Exec("PRAGMA synchronous=NORMAL")

	if err := initSchema(db); err != nil {
		log.Fatalf("初始化表失败: %v", err)
	}

	switch action {
	case "scan-symbols":
		if err := scanSymbols(db, decisionDir); err != nil {
			log.Fatalf("扫描失败: %v", err)
		}
	case "fetch-orders":
		if apiKey == "" || secretKey == "" {
			log.Fatalf("fetch-orders 需要 api_key 与 secret_key")
		}
		if err := fetchOrdersLoop(db, apiKey, secretKey, time.Duration(intervalSec)*time.Second, base); err != nil {
			log.Fatalf("拉取订单失败: %v", err)
		}
	case "fetch-orders-db":
		if err := fetchOrdersFromConfigDB(db, configDBPath, userID, exchangeID, time.Duration(intervalSec)*time.Second, base); err != nil {
			log.Fatalf("从配置库拉取订单失败: %v", err)
		}
	case "reconcile":
		if err := reconcileLogs(db, decisionDir); err != nil {
			log.Fatalf("对账失败: %v", err)
		}
	case "partial-close-reconcile":
		if err := reconcilePartialClose(db, decisionDir); err != nil {
			log.Fatalf("部分平仓对账失败: %v", err)
		}
	default:
		log.Fatalf("未知 action: %s", action)
	}
}

func initSchema(db *sql.DB) error {
	_, err := db.Exec(createSchema)
	return err
}

// scanSymbols 扫描日志目录收集开仓交易对
func scanSymbols(db *sql.DB, decisionDir string) error {
	totalCollected := 0 // 总共遇到的符号次数
	err := filepath.WalkDir(decisionDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		// 提取 trader_id (目录名)
		relPath, _ := filepath.Rel(decisionDir, path)
		parts := strings.Split(filepath.ToSlash(relPath), "/")
		if len(parts) < 2 {
			return nil // 不在子目录中,跳过
		}
		traderID := parts[0]

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var rec DecisionRecordPart
		if json.Unmarshal(data, &rec) != nil {
			return nil
		}
		for _, act := range rec.Decisions {
			if !act.Success {
				continue
			}
			if act.Action == "open_long" || act.Action == "open_short" {
				symbol := strings.TrimSpace(act.Symbol)
				if symbol == "" {
					continue
				}
				totalCollected++
				_, _ = db.Exec(`INSERT OR IGNORE INTO symbols(trader_id, symbol, first_seen) VALUES(?,?,?)`,
					traderID, symbol, time.Now().UnixMilli())
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// 统计去重后的实际符号数
	var uniqueCount int
	row := db.QueryRow(`SELECT COUNT(*) FROM symbols`)
	_ = row.Scan(&uniqueCount)

	log.Printf("✓ 已收集符号: %d 次开仓 → 去重后 %d 个交易对", totalCollected, uniqueCount)
	return nil
}

// fetchOrdersLoop 按顺序轮询 symbols 表
func fetchOrdersLoop(db *sql.DB, apiKey, secretKey string, interval time.Duration, base string) error {
	rows, err := db.Query(`SELECT trader_id, symbol FROM symbols ORDER BY trader_id, symbol`)
	if err != nil {
		return err
	}
	defer rows.Close()
	client := newSignedClient(apiKey, secretKey, base)
	for rows.Next() {
		var traderID, symbol string
		if err := rows.Scan(&traderID, &symbol); err != nil {
			continue
		}
		if err := fetchOrdersForSymbol(db, client, traderID, symbol); err != nil {
			log.Printf("⚠ 拉取 [%s] %s 失败: %v", traderID, symbol, err)
		}
		log.Printf("等待 %v 后继续...", interval)
		time.Sleep(interval)
	}
	return nil
}

// fetchOrdersFromConfigDB 读取 config.db 中的交易员与密钥，按交易员隔离拉取其 symbols 的订单
func fetchOrdersFromConfigDB(reconcileDB *sql.DB, configDBPath, userID, exchangeID string, interval time.Duration, base string) error {
	cfgDB, err := sql.Open("sqlite", configDBPath)
	if err != nil {
		return fmt.Errorf("打开配置数据库失败: %w", err)
	}
	defer cfgDB.Close()

	// 读取所有使用 binance 的交易员及其密钥（忽略空密钥）
	rows, err := cfgDB.Query(`
 		SELECT t.id AS trader_id, e.api_key, e.secret_key
 		FROM traders t
 		JOIN exchanges e ON t.exchange_id = e.id AND t.user_id = e.user_id
 		WHERE t.user_id = ? AND t.exchange_id = 'binance' AND COALESCE(e.api_key,'') <> '' AND COALESCE(e.secret_key,'') <> ''
 		ORDER BY t.id
 	`, userID)
	if err != nil {
		return fmt.Errorf("查询交易员密钥失败: %w", err)
	}
	defer rows.Close()

	log.Printf("🔎 从配置库读取交易员与密钥: db=%s, user_id=%s, base=%s", configDBPath, userID, base)
	foundTraders := 0
	processedSymbols := 0
	failedTasks := 0

	for rows.Next() {
		var traderID, apiKey, secretKey string
		if err := rows.Scan(&traderID, &apiKey, &secretKey); err != nil {
			failedTasks++
			log.Printf("⚠ 读取交易员行失败: %v", err)
			continue
		}
		foundTraders++
		// 查询该交易员的所有已扫描 symbol
		var symCount int
		if err := reconcileDB.QueryRow(`SELECT COUNT(*) FROM symbols WHERE trader_id = ?`, traderID).Scan(&symCount); err != nil {
			failedTasks++
			log.Printf("⚠ 读取交易员 %s 的符号数失败: %v", traderID, err)
			continue
		}
		if symCount == 0 {
			log.Printf("ℹ 交易员 %s 尚未扫描到任何符号，请先执行: go run ./tools/log_reconcile -action scan-symbols", traderID)
			continue
		}
		log.Printf("▶ 开始拉取交易员 %s（%d 个符号）", traderID, symCount)

		symRows, err := reconcileDB.Query(`SELECT symbol FROM symbols WHERE trader_id = ? ORDER BY symbol`, traderID)
		if err != nil {
			log.Printf("⚠ 读取交易员 %s 的符号失败: %v", traderID, err)
			continue
		}
		client := newSignedClient(apiKey, secretKey, base)
		for symRows.Next() {
			var symbol string
			if err := symRows.Scan(&symbol); err != nil {
				failedTasks++
				log.Printf("⚠ 解析符号行失败: %v", err)
				continue
			}
			if err := fetchOrdersForSymbol(reconcileDB, client, traderID, symbol); err != nil {
				log.Printf("⚠ 拉取 [%s] %s 失败: %v", traderID, symbol, err)
				failedTasks++
			}
			log.Printf("等待 %v 后继续...", interval)
			time.Sleep(interval)
			processedSymbols++
		}
		_ = symRows.Close()
	}

	if foundTraders == 0 {
		log.Printf("ℹ 未找到绑定到交易员的 Binance 密钥，尝试回退到按交易所拉取...")
		// 回退：直接使用 exchanges 中的 binance 账户对所有已扫描的 trader_id 拉取
		var exRows *sql.Rows
		var errEx error
		if strings.TrimSpace(exchangeID) != "" {
			exRows, errEx = cfgDB.Query(`SELECT id, api_key, secret_key FROM exchanges WHERE user_id = ? AND id = ? AND COALESCE(api_key,'')<>'' AND COALESCE(secret_key,'')<>''`, userID, exchangeID)
		} else {
			exRows, errEx = cfgDB.Query(`SELECT id, api_key, secret_key FROM exchanges WHERE user_id = ? AND type = 'binance' AND COALESCE(api_key,'')<>'' AND COALESCE(secret_key,'')<>'' ORDER BY id`, userID)
		}
		if errEx != nil {
			log.Printf("⚠ 查询交易所密钥失败: %v", errEx)
			log.Printf("✅ 完成: 交易员=%d, 符号处理=%d, 错误=%d", foundTraders, processedSymbols, failedTasks)
			return nil
		}
		defer exRows.Close()
		exs := make([]struct{ id, api, sec string }, 0)
		for exRows.Next() {
			var id, a, s string
			if err := exRows.Scan(&id, &a, &s); err == nil {
				exs = append(exs, struct{ id, api, sec string }{id, a, s})
			}
		}
		if len(exs) == 0 {
			log.Printf("ℹ 未在 exchanges 找到可用的 Binance 密钥。请配置 api_key/secret_key 或在命令行指定 -exchange_id。")
			log.Printf("✅ 完成: 交易员=%d, 符号处理=%d, 错误=%d", foundTraders, processedSymbols, failedTasks)
			return nil
		}
		if strings.TrimSpace(exchangeID) == "" && len(exs) > 1 {
			log.Printf("⚠ 检测到多个 Binance 账户: %d 个。为避免歧义，请使用 -exchange_id 指定一个（例如 -exchange_id %s）。", len(exs), exs[0].id)
			log.Printf("✅ 完成: 交易员=%d, 符号处理=%d, 错误=%d", foundTraders, processedSymbols, failedTasks)
			return nil
		}
		// 使用选定的 exchange
		chosen := exs[0]
		if strings.TrimSpace(exchangeID) != "" {
			for _, ex := range exs {
				if ex.id == exchangeID {
					chosen = ex
					break
				}
			}
		}
		log.Printf("↩ 回退使用交易所[%s]的密钥对所有已扫描交易员拉取", chosen.id)
		// 获取已扫描的 trader_id 列表
		idRows, err := reconcileDB.Query(`SELECT DISTINCT trader_id FROM symbols ORDER BY trader_id`)
		if err != nil {
			log.Printf("⚠ 读取已扫描的交易员列表失败: %v", err)
			log.Printf("✅ 完成: 交易员=%d, 符号处理=%d, 错误=%d", foundTraders, processedSymbols, failedTasks)
			return nil
		}
		defer idRows.Close()
		client := newSignedClient(chosen.api, chosen.sec, base)
		for idRows.Next() {
			var traderID string
			if err := idRows.Scan(&traderID); err != nil {
				failedTasks++
				continue
			}
			symRows, err := reconcileDB.Query(`SELECT symbol FROM symbols WHERE trader_id = ? ORDER BY symbol`, traderID)
			if err != nil {
				log.Printf("⚠ 读取交易员 %s 的符号失败: %v", traderID, err)
				failedTasks++
				continue
			}
			cnt := 0
			for symRows.Next() {
				var symbol string
				if err := symRows.Scan(&symbol); err != nil {
					failedTasks++
					continue
				}
				if err := fetchOrdersForSymbol(reconcileDB, client, traderID, symbol); err != nil {
					log.Printf("⚠ 拉取 [%s] %s 失败: %v", traderID, symbol, err)
					failedTasks++
				}
				time.Sleep(interval)
				processedSymbols++
				cnt++
			}
			_ = symRows.Close()
			log.Printf("⟲ 完成交易员 %s 的拉取（%d 个符号）", traderID, cnt)
		}
	}

	log.Printf("✅ 完成: 交易员=%d, 符号处理=%d, 错误=%d", foundTraders, processedSymbols, failedTasks)
	return nil
}

// fetchOrdersForSymbol 调用 allOrders
func fetchOrdersForSymbol(db *sql.DB, client *binanceREST, traderID, symbol string) error {
	st := time.Now()
	// 读取增量状态
	var lastOrderID sql.NullInt64
	row := db.QueryRow(`SELECT last_order_id FROM reconcile_state WHERE trader_id = ? AND symbol = ?`, traderID, symbol)
	_ = row.Scan(&lastOrderID)

	var all []BinanceOrder
	var rawAll []map[string]any
	// 若有 lastOrderID 直接使用 orderId 参数获取后续订单
	if lastOrderID.Valid && lastOrderID.Int64 > 0 {
		orders, raw, err := client.allOrders(symbol, lastOrderID.Int64, 0, 0)
		if err != nil {
			return err
		}
		all = append(all, orders...)
		rawAll = append(rawAll, raw...)
	} else {
		// 初次：按时间窗口分段（最多最近 30 天向后，接口每次最大 7 天）
		end := time.Now().UnixMilli()
		start := end - 7*24*3600*1000 // 最近 7 天即可，避免过多权重
		orders, raw, err := client.allOrders(symbol, 0, start, end)
		if err != nil {
			return err
		}
		all = append(all, orders...)
		rawAll = append(rawAll, raw...)
	}

	if len(all) == 0 {
		log.Printf("✓ [%s] %s 无新订单", traderID, symbol)
		return nil
	}

	// 使用事务批量写入，避免数据库锁定
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO orders(trader_id, symbol, order_id, side, position_side, status, avg_price, executed_qty, orig_qty, reduce_only, close_position, type, time, update_time, raw_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("准备语句失败: %w", err)
	}
	defer stmt.Close()

	for i, ord := range all {
		b, _ := json.Marshal(rawAll[i])
		avg := parseFloat(ord.AvgPrice)
		exec := parseFloat(ord.ExecutedQty)
		orig := parseFloat(ord.OrigQty)
		_, e := stmt.Exec(traderID, symbol, ord.OrderID, ord.Side, ord.PositionSide, ord.Status, avg, exec, orig,
			boolToInt(ord.ReduceOnly), boolToInt(ord.ClosePosition), ord.Type, ord.Time, ord.UpdateTime, string(b))
		if e != nil {
			log.Printf("⚠ 写入订单失败 [%s] %s order_id=%d: %v", traderID, symbol, ord.OrderID, e)
		}
	}

	// 更新状态
	_, err = tx.Exec(`INSERT OR REPLACE INTO reconcile_state(trader_id, symbol, last_order_id, last_fetch_time) VALUES(?,?,?,?)`,
		traderID, symbol, latestOrderID(all), time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("更新状态失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	log.Printf("✓ [%s] %s 增量拉取 %d 条, 用时 %v", traderID, symbol, len(all), time.Since(st))
	return nil
}

// reconcileLogs placeholder
func reconcileLogs(db *sql.DB, decisionDir string) error {
	// 读取订单缓存
	ordersMap, err := loadOrdersGrouped(db)
	if err != nil {
		return err
	}

	// 遍历 trader 子目录（decision_logs 下的目录）
	entries, err := os.ReadDir(decisionDir)
	if err != nil {
		return fmt.Errorf("读取决策目录失败: %w", err)
	}

	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		traderID := ent.Name()
		traderPath := filepath.Join(decisionDir, traderID)
		if err := reconcileTrader(traderPath, traderID, ordersMap); err != nil {
			log.Printf("⚠ 对账 %s 失败: %v", traderPath, err)
		}
	}
	return nil
}

// loadOrdersGrouped 按 trader_id+symbol+position_side 分组订单（已按时间排序）
func loadOrdersGrouped(db *sql.DB) (map[string][]BinanceOrder, error) {
	// 读取订单缓存
	rows, err := db.Query(`SELECT trader_id, symbol, order_id, side, position_side, status, avg_price, executed_qty, orig_qty, reduce_only, close_position, type, time, update_time, raw_json FROM orders`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := make(map[string][]BinanceOrder)
	for rows.Next() {
		// 重建部分字段
		var o BinanceOrder
		var traderID, symbol string
		var avg, exec, orig float64
		var reduceOnly, closePos int
		var raw string
		// 重建部分字段
		if err := rows.Scan(&traderID, &symbol, &o.OrderID, &o.Side, &o.PositionSide, &o.Status, &avg, &exec, &orig, &reduceOnly, &closePos, &o.Type, &o.Time, &o.UpdateTime, &raw); err != nil {
			continue
		}
		o.Symbol = symbol
		// 直接用 strconv.FormatFloat 保持精度
		o.ExecutedQty = strconv.FormatFloat(exec, 'f', -1, 64)
		o.OrigQty = strconv.FormatFloat(orig, 'f', -1, 64)
		o.AvgPrice = strconv.FormatFloat(avg, 'f', -1, 64)
		// 如果 AvgPrice 为 0，尝试从 raw_json 解析 price
		if avg == 0 && raw != "" {
			var rawData map[string]interface{}
			if json.Unmarshal([]byte(raw), &rawData) == nil {
				if priceStr, ok := rawData["price"].(string); ok {
					o.Price = priceStr
				}
			}
		}
		o.ReduceOnly = reduceOnly == 1
		o.ClosePosition = closePos == 1
		// key = trader_id + symbol + position_side
		key := traderID + "_" + symbol + "_" + strings.ToUpper(o.PositionSide)
		res[key] = append(res[key], o)
	}
	// 排序
	for k := range res {
		sort.Slice(res[k], func(i, j int) bool { return res[k][i].Time < res[k][j].Time })
	}
	return res, nil
}

// reconcileTrader 针对单个 trader 日志目录执行校验与补全
func reconcileTrader(dir string, traderID string, orders map[string][]BinanceOrder) error {
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	// 收集日志记录
	var logFiles []string
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		logFiles = append(logFiles, filepath.Join(dir, f.Name()))
	}
	// 解析并构建开/平仓状态
	openPositions := make(map[string]DecisionAction) // key=symbol_side
	closedPositions := make(map[string]bool)
	fileActions := make(map[string][]DecisionAction) // 文件到动作列表

	for _, fp := range logFiles {
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		var rec DecisionRecordPart
		if json.Unmarshal(data, &rec) != nil {
			continue
		}
		for i, act := range rec.Decisions {
			if !act.Success {
				continue
			}
			fileActions[fp] = append(fileActions[fp], act)
			if act.Action == "open_long" || act.Action == "open_short" {
				key := act.Symbol + "_" + sideFromAction(act.Action)
				openPositions[key] = act
			} else if isCloseAction(act.Action) {
				key := act.Symbol + "_" + sideFromAction(act.Action)
				closedPositions[key] = true
			}
			// partial_close 暂不特殊处理
			_ = i
		}
	}

	// 查找缺失的平仓
	for key, openAct := range openPositions {
		if closedPositions[key] {
			continue
		}
		// 根据 trader_id+key 获取订单候选
		ordKey := traderID + "_" + key
		ordList := orders[ordKey]
		if len(ordList) == 0 {
			continue
		}
		// 选择开仓时间后最近的一个 closePosition 或 reduceOnly 订单
		var best *BinanceOrder
		for i := range ordList {
			o := ordList[i]
			if o.Time < openAct.Timestamp.UnixMilli() {
				continue
			}
			// 判断是否是平仓候选
			if !(o.ClosePosition || o.ReduceOnly) {
				continue
			}
			// Side 应与开仓对应的平仓方向相反
			if !matchCloseSide(openAct.Action, o.Side) {
				continue
			}
			// 🔧 只使用已完全成交的订单 (FILLED)
			if strings.ToUpper(o.Status) != "FILLED" {
				continue
			}
			// 🔧 确保有成交数量和价格
			qty := parseFloat(o.ExecutedQty)
			price := safePrice(&o)
			if qty <= 0 || price <= 0 {
				continue
			}
			best = &o
			break
		}
		if best == nil {
			continue
		}
		// 生成补全文件
		// 如果是 reduceOnly 且非 closePosition，按业务语义更贴近 "partial_close"
		actionName := closeActionName(openAct.Action)
		if best.ReduceOnly && !best.ClosePosition {
			actionName = "partial_close"
		}
		closeAction := DecisionAction{
			Action:    actionName,
			Symbol:    openAct.Symbol,
			Quantity:  parseFloat(best.ExecutedQty),
			Price:     safePrice(best),
			OrderID:   best.OrderID,
			Timestamp: time.UnixMilli(best.Time),
			Success:   true,
		}
		// 写入新文件 decision_reconcile_*
		fname := fmt.Sprintf("decision_reconcile_%s_%d.json", time.Now().Format("20060102_150405"), best.OrderID)
		path := filepath.Join(dir, fname)
		rec := DecisionRecordPart{Decisions: []DecisionAction{closeAction}}
		b, _ := json.MarshalIndent(rec, "", "  ")
		if err := os.WriteFile(path, b, 0644); err != nil {
			log.Printf("⚠ 写入补全文件失败 %s: %v", path, err)
		} else {
			log.Printf("➕ 已补全平仓: %s → %s", key, path)
		}
	}

	// 校正已有的开仓行为
	var openMismatches []string
	for fp, acts := range fileActions {
		changed := false
		for i, act := range acts {

			// 处理开仓
			if act.Action == "open_long" || act.Action == "open_short" {
				// 订单候选：优先使用对应方向，其次回退 BOTH
				lists := getOrderLists(orders, traderID, act.Symbol, sideFromAction(act.Action))
				var candidate *BinanceOrder
				bestDelta := int64(1<<62 - 1)
				for _, ordList := range lists {
					for idx := range ordList {
						o := ordList[idx]
						// 开仓方向匹配：open_long -> BUY/LONG, open_short -> SELL/SHORT
						if !matchOpenSide(act.Action, o.Side) {
							continue
						}
						// 时间容差
						delta := abs64(o.Time - act.Timestamp.UnixMilli())
						if delta > timeToleranceMs {
							continue
						}
						// 开仓订单不应该是 reduceOnly 或 closePosition
						if o.ClosePosition || o.ReduceOnly {
							continue
						}
						// 必须完全成交，且有数量与价格
						if strings.ToUpper(o.Status) != "FILLED" {
							continue
						}
						qty := parseFloat(o.ExecutedQty)
						price := safePrice(&o)
						if qty <= 0 || price <= 0 {
							continue
						}
						if delta < bestDelta {
							bestDelta = delta
							candidate = &o
						}
					}
				}
				if candidate == nil {
					openMismatches = append(openMismatches, fmt.Sprintf("⚠ [%s] %s %s 未找到匹配的开仓订单 (决策时间: %s, 价格: %.4f, 数量: %.4f) → 改为 wait",
						traderID, act.Symbol, act.Action, act.Timestamp.Format("2006-01-02 15:04:05"), act.Price, act.Quantity))
					// 输出调试信息：显示所有候选订单的时间差异
					log.Printf("⏰ [调试] %s %s 时间对比:", act.Symbol, act.Action)
					log.Printf("   决策记录时间: %s", act.Timestamp.Format("2006-01-02 15:04:05"))
					for _, ordList := range lists {
						for idx, o := range ordList {
							if idx >= 5 {
								break
							}
							diffMinutes := float64(o.Time-act.Timestamp.UnixMilli()) / 60000
							log.Printf("   订单 %d (ID:%d): %s (时间差 %.1f分钟, 方向:%s, 状态:%s)",
								idx+1, o.OrderID,
								time.UnixMilli(o.Time).Format("2006-01-02 15:04:05"),
								diffMinutes, o.Side, o.Status)
						}
					}
					// 🔧 将无法匹配的开仓操作改为 wait
					acts[i].Action = "wait"
					acts[i].OrderID = 0
					acts[i].Quantity = 0
					acts[i].Price = 0
					changed = true
					continue
				}
				qty := parseFloat(candidate.ExecutedQty)
				price := safePrice(candidate)
				// 检查偏差
				qtyDev := deviation(act.Quantity, qty)
				priceDev := deviation(act.Price, price)
				if qtyDev > 0.01 || priceDev > 0.01 {
					openMismatches = append(openMismatches, fmt.Sprintf("📝 [%s] %s %s 数据偏差: 数量 %.4f→%.4f (%.2f%%), 价格 %.4f→%.4f (%.2f%%)",
						traderID, act.Symbol, act.Action, act.Quantity, qty, qtyDev*100, act.Price, price, priceDev*100))
					acts[i].Quantity = qty
					acts[i].Price = price
					acts[i].OrderID = candidate.OrderID
					acts[i].Timestamp = time.UnixMilli(candidate.Time)
					changed = true
				} else if act.OrderID != candidate.OrderID {
					// 价格数量一致但 OrderID 不同
					openMismatches = append(openMismatches, fmt.Sprintf("🔧 [%s] %s %s OrderID 不匹配: %d→%d",
						traderID, act.Symbol, act.Action, act.OrderID, candidate.OrderID))
					acts[i].OrderID = candidate.OrderID
					changed = true
				}
			}
			// 处理平仓
			if isCloseAction(act.Action) {
				lists := getOrderLists(orders, traderID, act.Symbol, sideFromAction(act.Action))
				var candidate *BinanceOrder
				bestDelta := int64(1<<62 - 1)
				for _, ordList := range lists {
					for idx := range ordList {
						o := ordList[idx]
						if !matchCloseSide(act.Action, o.Side) {
							continue
						}
						delta := abs64(o.Time - act.Timestamp.UnixMilli())
						if delta > timeToleranceMs {
							continue
						}
						if !(o.ClosePosition || o.ReduceOnly) {
							continue
						}
						if strings.ToUpper(o.Status) != "FILLED" {
							continue
						}
						qty := parseFloat(o.ExecutedQty)
						price := safePrice(&o)
						if qty <= 0 || price <= 0 {
							continue
						}
						if delta < bestDelta {
							bestDelta = delta
							candidate = &o
						}
					}
				}
				if candidate == nil {
					// 🔧 将无法匹配的平仓操作改为 wait
					openMismatches = append(openMismatches, fmt.Sprintf("⚠ [%s] %s %s 未找到匹配的平仓订单 (决策时间: %s) → 改为 wait",
						traderID, act.Symbol, act.Action, act.Timestamp.Format("2006-01-02 15:04:05")))
					acts[i].Action = "wait"
					acts[i].OrderID = 0
					acts[i].Quantity = 0
					acts[i].Price = 0
					changed = true
					continue
				}
				qty := parseFloat(candidate.ExecutedQty)
				price := safePrice(candidate)
				if deviation(act.Quantity, qty) > 0.01 || deviation(act.Price, price) > 0.01 {
					acts[i].Quantity = qty
					acts[i].Price = price
					acts[i].OrderID = candidate.OrderID
					acts[i].Timestamp = time.UnixMilli(candidate.Time)
					changed = true
				}
			}

			// 处理 partial_close - 也需要匹配实际订单
			if act.Action == "partial_close" {
				// 同时在 LONG/SHORT 列表中寻找 reduce_only 的部分平仓成交
				listsLong := getOrderLists(orders, traderID, act.Symbol, "LONG")
				listsShort := getOrderLists(orders, traderID, act.Symbol, "SHORT")
				var candidate *BinanceOrder
				bestDelta := int64(1<<62 - 1)
				check := func(ordList []BinanceOrder, closeAction string) {
					for idx := range ordList {
						o := ordList[idx]
						if !matchCloseSide(closeAction, o.Side) {
							continue
						}
						delta := abs64(o.Time - act.Timestamp.UnixMilli())
						if delta > timeToleranceMs {
							continue
						}
						if !o.ReduceOnly {
							continue
						}
						// 接受 FILLED，或 PARTIALLY_FILLED/CANCELED 但有成交数量的部分平仓
						statusU := strings.ToUpper(o.Status)
						qty := parseFloat(o.ExecutedQty)
						if !(statusU == "FILLED" || ((statusU == "PARTIALLY_FILLED" || statusU == "CANCELED") && qty > 0)) {
							continue
						}
						price := safePrice(&o)
						if qty <= 0 || price <= 0 {
							continue
						}
						if delta < bestDelta {
							bestDelta = delta
							candidate = &o
						}
					}
				}
				for _, l := range listsLong {
					check(l, "close_long")
				}
				for _, l := range listsShort {
					check(l, "close_short")
				}
				if candidate == nil {
					openMismatches = append(openMismatches, fmt.Sprintf("⚠ [%s] %s partial_close 未找到匹配订单 → 改为 wait", traderID, act.Symbol))
					acts[i].Action = "wait"
					acts[i].OrderID = 0
					acts[i].Quantity = 0
					acts[i].Price = 0
					changed = true
					continue
				}
			}

			// 处理其他操作类型 (hold, update_stop_loss, update_take_profit 等)
			// 这些操作不需要实际订单,但如果标记为 success 但没有对应的真实交易操作,也改为 wait
			if !needsOrderMatch(act.Action) && act.Action != "wait" && act.Action != "hold" {
				// update_stop_loss, update_take_profit 等操作应该有对应的修改订单记录
				// 但由于 allOrders 接口可能不包含这些修改,暂时保留原样
				// 如果未来需要验证,可以在这里添加逻辑
			}
		}
		if changed {
			// 备份原文件
			_ = os.Rename(fp, fp+".bak")
			// 读取原文件其余字段并只替换 decisions
			if err := writeUpdatedFilePreserve(fp+".bak", fp, acts); err != nil {
				log.Printf("⚠ 覆盖文件失败 %s: %v", fp, err)
			} else {
				log.Printf("✏ 已校正文件 %s", fp)
			}
		}
	}

	// 输出开仓不匹配报告
	if len(openMismatches) > 0 {
		reportPath := filepath.Join(dir, fmt.Sprintf("open_mismatch_report_%s.txt", time.Now().Format("20060102_150405")))
		reportContent := strings.Join(append([]string{"=== 开仓数据核对报告 ===", fmt.Sprintf("生成时间: %s", time.Now().Format("2006-01-02 15:04:05")), ""}, openMismatches...), "\n")
		if err := os.WriteFile(reportPath, []byte(reportContent), 0644); err != nil {
			log.Printf("⚠ 写入开仓不匹配报告失败: %v", err)
		} else {
			log.Printf("📊 已生成开仓不匹配报告: %s (%d 条)", reportPath, len(openMismatches))
		}
		// 同时输出到日志
		for _, msg := range openMismatches {
			log.Println(msg)
		}
	}

	return nil
}

// ---------- 辅助 ----------
func sideFromAction(action string) string {
	if strings.Contains(action, "long") {
		return "LONG"
	}
	return "SHORT"
}

func isCloseAction(action string) bool {
	return strings.HasPrefix(action, "close_") || strings.HasPrefix(action, "auto_close_")
}

func needsOrderMatch(action string) bool {
	// 需要匹配实际订单的操作类型
	return action == "open_long" || action == "open_short" ||
		isCloseAction(action) ||
		action == "partial_close"
}

func closeActionName(openAction string) string {
	if openAction == "open_long" {
		return "close_long"
	}
	return "close_short" // open_short 对应 close_short
}

func matchOpenSide(action string, orderSide string) bool {
	// open_long -> 开仓应是 BUY; open_short -> 开仓应是 SELL
	isLong := strings.Contains(action, "long")
	if isLong {
		return strings.ToUpper(orderSide) == "BUY"
	}
	return strings.ToUpper(orderSide) == "SELL"
}

func matchCloseSide(actionOrOpen string, orderSide string) bool {
	// open_long -> 平仓应是 SELL; open_short -> 平仓应是 BUY
	// close_long 同理 SELL, close_short BUY
	isLong := strings.Contains(actionOrOpen, "long")
	if isLong {
		return strings.ToUpper(orderSide) == "SELL"
	}
	return strings.ToUpper(orderSide) == "BUY"
}

func safePrice(o *BinanceOrder) float64 {
	avg := parseFloat(o.AvgPrice)
	if avg > 0 {
		return avg
	}
	return parseFloat(o.Price)
}

func deviation(a, b float64) float64 {
	if a == 0 && b == 0 {
		return 0
	}
	den := math.Max(math.Abs(a), math.Abs(b))
	return math.Abs(a-b) / den
}

// 获取订单列表：优先 position_side，回退 BOTH
func getOrderLists(group map[string][]BinanceOrder, traderID, symbol, posSide string) [][]BinanceOrder {
	var res [][]BinanceOrder
	key := traderID + "_" + symbol + "_" + strings.ToUpper(posSide)
	if lst, ok := group[key]; ok && len(lst) > 0 {
		res = append(res, lst)
	}
	// 兜底：一向模式 positionSide=BOTH
	keyBoth := traderID + "_" + symbol + "_BOTH"
	if lst, ok := group[keyBoth]; ok && len(lst) > 0 {
		res = append(res, lst)
	}
	return res
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// ========= 工具函数 =========

// writeUpdatedFilePreserve 读取 src JSON，保留除 decisions 外的所有顶层字段，仅替换 decisions 后写入 dst
func writeUpdatedFilePreserve(srcPath, dstPath string, newActs []DecisionAction) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		// 回退：若不是对象结构，直接写最小结构
		rec := DecisionRecordPart{Decisions: newActs}
		b, _ := json.MarshalIndent(rec, "", "  ")
		return os.WriteFile(dstPath, b, 0644)
	}
	obj["decisions"] = newActs
	b, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dstPath, b, 0644)
}

func parseFloat(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func latestOrderID(list []BinanceOrder) int64 {
	var m int64
	for _, o := range list {
		if o.OrderID > m {
			m = o.OrderID
		}
	}
	return m
}

// ========== 币安 REST 签名客户端（最小实现 allOrders） ==========

// 需要的导入
// (为保持文件紧凑，上方 import 未包含下面依赖, 合并时请确保添加)

// 重新整理 import 以避免遗漏
// --- 我们在顶部已 import 需要的包 ---

// binanceREST 简化客户端

type binanceREST struct {
	apiKey    string
	secretKey string
	baseURL   string
	client    *http.Client
}

func newSignedClient(apiKey, secretKey, base string) *binanceREST {
	url := "https://dapi.binance.com" // USDⓈ-M: fapi  / 币本位交割合约: dapi
	if base == "fapi" {
		url = "https://fapi.binance.com"
	}
	return &binanceREST{apiKey: apiKey, secretKey: secretKey, baseURL: url, client: &http.Client{Timeout: 15 * time.Second}}
}

func (c *binanceREST) allOrders(symbol string, orderID, startTime, endTime int64) ([]BinanceOrder, []map[string]any, error) {
	if symbol == "" {
		return nil, nil, errors.New("symbol 不能为空")
	}

	params := []string{fmt.Sprintf("symbol=%s", symbol)}
	if orderID > 0 {
		params = append(params, fmt.Sprintf("orderId=%d", orderID))
	}
	if startTime > 0 {
		params = append(params, fmt.Sprintf("startTime=%d", startTime))
	}
	if endTime > 0 {
		params = append(params, fmt.Sprintf("endTime=%d", endTime))
	}
	params = append(params, fmt.Sprintf("timestamp=%d", time.Now().UnixMilli()))
	qs := strings.Join(params, "&")
	// 签名
	sig := hmacSHA256Hex(qs, c.secretKey)
	path := "/dapi/v1/allOrders"
	if strings.Contains(c.baseURL, "fapi") {
		path = "/fapi/v1/allOrders"
	}
	url := fmt.Sprintf("%s%s?%s&signature=%s", c.baseURL, path, qs, sig)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	req.Header.Set("X-MBX-APIKEY", c.apiKey)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var raw []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, nil, err
	}
	var list []BinanceOrder
	for _, r := range raw {
		b, _ := json.Marshal(r)
		var bo BinanceOrder
		if json.Unmarshal(b, &bo) == nil {
			list = append(list, bo)
		}
	}
	return list, raw, nil
}

// 签名

func hmacSHA256Hex(data, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// ===== 缺失 import 的补充 =====
// 为保持结构清晰，这些放在文件末尾避免多次滚动
// 已在顶部 import 所需包，无需重复

// （reconcilePartialClose 的实现位于 partial_close_reconcile.go）
