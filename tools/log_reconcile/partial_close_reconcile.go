package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// PartialCloseAction 部分平仓记录
type PartialCloseAction struct {
	Action          string    `json:"action"`
	Symbol          string    `json:"symbol"`
	ClosePercentage float64   `json:"close_percentage"`
	Price           float64   `json:"price"`
	Quantity        float64   `json:"quantity"` // 实际平仓数量（如果有）
	OrderID         int64     `json:"order_id"`
	Timestamp       time.Time `json:"timestamp"`
	Success         bool      `json:"success"`
	Error           string    `json:"error"`
}

// DecisionJSONItem 决策JSON中的单个决策项
type DecisionJSONItem struct {
	Symbol          string  `json:"symbol"`
	Action          string  `json:"action"`
	ClosePercentage float64 `json:"close_percentage,omitempty"`
	Confidence      float64 `json:"confidence,omitempty"`
	Reasoning       string  `json:"reasoning"`
	Price           float64 `json:"price,omitempty"`
}

// PositionTracker 仓位跟踪器
type PositionTracker struct {
	Symbol        string
	Side          string // LONG/SHORT
	OpenQty       float64
	OpenPrice     float64
	OpenTime      time.Time
	PartialCloses []PartialCloseAction
	TotalClosed   float64 // 累计平仓数量
	FullCloseTime time.Time
	FullCloseQty  float64
}

// reconcilePartialClose 对账部分平仓
func reconcilePartialClose(db *sql.DB, decisionDir string) error {
	log.Println("=== 开始部分平仓对账 ===")

	// 读取订单缓存
	ordersMap, err := loadOrdersGrouped(db)
	if err != nil {
		return fmt.Errorf("加载订单失败: %w", err)
	}

	// 遍历 trader 子目录
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
		if err := reconcilePartialCloseForTrader(traderPath, traderID, ordersMap); err != nil {
			log.Printf("⚠ 对账 %s 部分平仓失败: %v", traderPath, err)
		}
	}

	return nil
}

// reconcilePartialCloseForTrader 针对单个 trader 处理部分平仓
func reconcilePartialCloseForTrader(dir string, traderID string, orders map[string][]BinanceOrder) error {
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	// 收集所有日志文件
	var logFiles []string
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		logFiles = append(logFiles, filepath.Join(dir, f.Name()))
	}

	// 按时间排序文件
	sort.Strings(logFiles)

	// 构建仓位时间线
	positions := make(map[string]*PositionTracker) // key = symbol_side

	// 构建决策映射 (timestamp_symbol -> DecisionJSON)
	decisionMap := make(map[string][]DecisionJSONItem)

	for _, fp := range logFiles {
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		var rec DecisionRecordPart
		if json.Unmarshal(data, &rec) != nil {
			continue
		}

		// 解析 decision_json 字段
		if rec.DecisionJSON != "" {
			var decisionItems []DecisionJSONItem
			if err := json.Unmarshal([]byte(rec.DecisionJSON), &decisionItems); err == nil {
				// 使用时间戳作为key
				tsKey := rec.Timestamp.Format("2006-01-02T15:04:05")
				decisionMap[tsKey] = decisionItems
			}
		}

		for _, act := range rec.Decisions {
			if !act.Success {
				continue
			}

			// 开仓
			if act.Action == "open_long" || act.Action == "open_short" {
				side := sideFromAction(act.Action)
				key := act.Symbol + "_" + side
				positions[key] = &PositionTracker{
					Symbol:        act.Symbol,
					Side:          side,
					OpenQty:       act.Quantity,
					OpenPrice:     act.Price,
					OpenTime:      act.Timestamp,
					PartialCloses: []PartialCloseAction{},
				}
			}

			// 部分平仓
			if act.Action == "partial_close" {
				// 从决策JSON中查找对应的 close_percentage
				closePercentage := 0.0
				tsKey := rec.Timestamp.Format("2006-01-02T15:04:05")
				if decisions, ok := decisionMap[tsKey]; ok {
					for _, d := range decisions {
						if d.Action == "partial_close" && d.Symbol == act.Symbol {
							closePercentage = d.ClosePercentage
							break
						}
					}
				}

				// 尝试两种可能
				for _, side := range []string{"LONG", "SHORT"} {
					key := act.Symbol + "_" + side
					if pos, exists := positions[key]; exists && pos.FullCloseTime.IsZero() {
						partialClose := PartialCloseAction{
							Action:          act.Action,
							Symbol:          act.Symbol,
							ClosePercentage: closePercentage,
							Price:           act.Price,
							Quantity:        act.Quantity,
							OrderID:         act.OrderID,
							Timestamp:       act.Timestamp,
							Success:         act.Success,
						}
						pos.PartialCloses = append(pos.PartialCloses, partialClose)
						pos.TotalClosed += act.Quantity
						break
					}
				}
			}

			// 完全平仓
			if isCloseAction(act.Action) {
				side := sideFromAction(act.Action)
				key := act.Symbol + "_" + side
				if pos, exists := positions[key]; exists {
					pos.FullCloseTime = act.Timestamp
					pos.FullCloseQty = act.Quantity
				}
			}
		}
	}

	// 对账部分平仓
	var issues []string
	for key, pos := range positions {
		if len(pos.PartialCloses) == 0 {
			continue // 没有部分平仓，跳过
		}

		ordKey := traderID + "_" + key
		ordList := orders[ordKey]
		if len(ordList) == 0 {
			continue
		}

		// 验证部分平仓记录
		for i, pc := range pos.PartialCloses {
			matched := false
			for _, o := range ordList {
				// 时间匹配：±30分钟（使用 decisions 中的实际成交时间）
				// decisions[].timestamp 是实际下单成交时间，更接近币安订单时间
				if math.Abs(float64(o.Time-pc.Timestamp.UnixMilli())) > 30*60*1000 {
					continue
				}
				// 必须是 reduceOnly 或 closePosition
				if !o.ReduceOnly && !o.ClosePosition {
					continue
				}
				// 必须是 FILLED
				if strings.ToUpper(o.Status) != "FILLED" {
					continue
				}
				// Side 匹配
				if !matchCloseSide(pos.Side, o.Side) {
					continue
				}

				qty := parseFloat(o.ExecutedQty)
				price := safePrice(&o)
				if qty <= 0 || price <= 0 {
					continue
				}

				// 检查是否匹配
				qtyDev := deviation(pc.Quantity, qty)
				priceDev := deviation(pc.Price, price)

				if qtyDev > 0.05 || priceDev > 0.05 {
					issues = append(issues, fmt.Sprintf(
						"📝 [%s] %s partial_close #%d 数据偏差: 数量 %.4f→%.4f (%.2f%%), 价格 %.4f→%.4f (%.2f%%), 时间: %s",
						traderID, key, i+1, pc.Quantity, qty, qtyDev*100, pc.Price, price, priceDev*100,
						pc.Timestamp.Format("2006-01-02 15:04:05")))
				} else if pc.OrderID != o.OrderID {
					issues = append(issues, fmt.Sprintf(
						"🔧 [%s] %s partial_close #%d OrderID不匹配: %d→%d, 时间: %s",
						traderID, key, i+1, pc.OrderID, o.OrderID, pc.Timestamp.Format("2006-01-02 15:04:05")))
				}
				matched = true
				break
			}

			if !matched {
				issues = append(issues, fmt.Sprintf(
					"⚠ [%s] %s partial_close #%d 未找到匹配订单: 数量 %.4f, 价格 %.4f, 时间: %s",
					traderID, key, i+1, pc.Quantity, pc.Price, pc.Timestamp.Format("2006-01-02 15:04:05")))
			}
		}

		// 验证累计平仓数量
		expectedRemaining := pos.OpenQty - pos.TotalClosed
		if !pos.FullCloseTime.IsZero() {
			// 有完全平仓记录，检查是否匹配剩余数量
			qtyDev := deviation(expectedRemaining, pos.FullCloseQty)
			if qtyDev > 0.05 {
				issues = append(issues, fmt.Sprintf(
					"⚠ [%s] %s 累计平仓数量不匹配: 开仓 %.4f - 部分平仓 %.4f = 预期剩余 %.4f, 实际完全平仓 %.4f (偏差 %.2f%%)",
					traderID, key, pos.OpenQty, pos.TotalClosed, expectedRemaining, pos.FullCloseQty, qtyDev*100))
			}
		}
	}

	// 输出报告
	if len(issues) > 0 {
		reportPath := filepath.Join(dir, fmt.Sprintf("partial_close_report_%s.txt", time.Now().Format("20060102_150405")))
		reportContent := strings.Join(append([]string{
			"=== 部分平仓对账报告 ===",
			fmt.Sprintf("生成时间: %s", time.Now().Format("2006-01-02 15:04:05")),
			fmt.Sprintf("Trader ID: %s", traderID),
			"",
		}, issues...), "\n")

		if err := os.WriteFile(reportPath, []byte(reportContent), 0644); err != nil {
			log.Printf("⚠ 写入部分平仓报告失败: %v", err)
		} else {
			log.Printf("📊 [%s] 已生成部分平仓报告: %s (%d 条)", traderID, reportPath, len(issues))
		}

		// 输出到日志
		for _, msg := range issues {
			log.Println(msg)
		}
	} else {
		log.Printf("✓ [%s] 部分平仓对账通过，无异常", traderID)
	}

	return nil
}

// matchCloseSide 匹配平仓方向（从仓位方向判断）
func matchCloseSideFromPosition(positionSide string, orderSide string) bool {
	// LONG 仓位平仓应该是 SELL
	// SHORT 仓位平仓应该是 BUY
	if positionSide == "LONG" {
		return strings.ToUpper(orderSide) == "SELL"
	}
	return strings.ToUpper(orderSide) == "BUY"
}
