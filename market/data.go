package market

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Get 获取指定代币的市场数据
func Get(symbol string) (*Data, error) {
	var klines3m, klines4h []Kline
	var err error
	// 标准化symbol
	symbol = Normalize(symbol)
	// 获取3分钟K线数据 (最近10个)
	klines3m, err = WSMonitorCli.GetCurrentKlines(symbol, "3m") // 多获取一些用于计算
	if err != nil {
		return nil, fmt.Errorf("获取3分钟K线失败: %v", err)
	}

	// 获取4小时K线数据 (最近10个)
	klines4h, err = WSMonitorCli.GetCurrentKlines(symbol, "4h") // 多获取用于计算指标
	if err != nil {
		return nil, fmt.Errorf("获取4小时K线失败: %v", err)
	}

	// 新增15m数据
	klines15m, err := WSMonitorCli.GetCurrentKlines(symbol, "15m")
	if err != nil {
		return nil, fmt.Errorf("获取15分钟K线失败: %v", err)
	}

	// 新增1h数据
	klines1h, err := WSMonitorCli.GetCurrentKlines(symbol, "1h")
	if err != nil {
		return nil, fmt.Errorf("获取1小时K线失败: %v", err)
	}

	// 新增1d数据
	klines1d, err := WSMonitorCli.GetCurrentKlines(symbol, "1d")
	if err != nil {
		return nil, fmt.Errorf("获取1天K线失败: %v", err)
	}

	// 计算当前指标 (基于3分钟最新数据)
	currentPrice := klines3m[len(klines3m)-1].Close
	currentEMA20 := calculateEMA(klines3m, 20)
	dif, _, _ := calculateMACD(klines3m, 12, 26, 9)
	currentMACD := dif
	currentRSI7 := calculateRSI(klines3m, 7)

	// 计算价格变化百分比

	// 3分钟价格变化（当前与上一根3m）
	priceChange3m := 0.0
	if len(klines3m) >= 2 {
		prev := klines3m[len(klines3m)-2].Close
		if prev > 0 {
			priceChange3m = ((currentPrice - prev) / prev) * 100
		}
	}

	// 1小时价格变化 = 20个3分钟K线前的价格
	priceChange1h := 0.0
	if len(klines3m) >= 21 { // 至少需要21根K线 (当前 + 20根前)
		price1hAgo := klines3m[len(klines3m)-21].Close
		if price1hAgo > 0 {
			priceChange1h = ((currentPrice - price1hAgo) / price1hAgo) * 100
		}
	}

	// 4小时价格变化 = 1个4小时K线前的价格
	priceChange4h := 0.0
	if len(klines4h) >= 2 {
		price4hAgo := klines4h[len(klines4h)-2].Close
		if price4hAgo > 0 {
			priceChange4h = ((currentPrice - price4hAgo) / price4hAgo) * 100
		}
	}

	priceChange15m := 0.0
	if len(klines15m) >= 2 { // 15分钟前的价格（1根15分钟K线）
		price15mAgo := klines15m[len(klines15m)-2].Close
		if price15mAgo > 0 {
			priceChange15m = ((currentPrice - price15mAgo) / price15mAgo) * 100
		}
	}
	priceChange1d := 0.0
	if len(klines1d) >= 2 { // 1天前的价格（1根1天K线）
		price1dAgo := klines1d[len(klines1d)-2].Close
		if price1dAgo > 0 {
			priceChange1d = ((currentPrice - price1dAgo) / price1dAgo) * 100
		}
	}

	// 获取OI数据
	oiData, err := getOpenInterestData(symbol)
	if err != nil {
		// OI失败不影响整体,使用默认值
		oiData = &OIData{Latest: 0, Average: 0}
	}

	// 获取Funding Rate
	fundingRate, _ := getFundingRate(symbol)

	// 计算各时间框架的指标数据
	intradayData := calculateIntradaySeries(klines3m)   // 3分钟
	intraday15m := calculateIntradaySeries(klines15m)   // 15分钟
	intraday1h := calculateIntradaySeries(klines1h)     // 1小时
	longerTermData := calculateLongerTermData(klines4h) // 4小时
	longerTerm1d := calculateLongerTermData(klines1d)   // 1天

	return &Data{
		Symbol:            symbol,
		CurrentPrice:      currentPrice,
		PriceChange3m:     priceChange3m,
		PriceChange15m:    priceChange15m, // 新增
		PriceChange1h:     priceChange1h,
		PriceChange4h:     priceChange4h,
		PriceChange1d:     priceChange1d, // 新增
		CurrentEMA20:      currentEMA20,
		CurrentMACD:       currentMACD,
		CurrentRSI7:       currentRSI7,
		OpenInterest:      oiData,
		FundingRate:       fundingRate,
		IntradaySeries:    intradayData,
		LongerTermContext: longerTermData,
		Intraday15m:       intraday15m,  // 新增
		Intraday1h:        intraday1h,   // 新增
		LongerTerm1d:      longerTerm1d, // 新增
		EffortResult3m:    computeEffortResult(priceChange3m, intradayData, oiData.Change5m),
		EffortResult15m:   computeEffortResult(priceChange15m, intraday15m, oiData.Change15m),
		EffortResult1h:    computeEffortResult(priceChange1h, intraday1h, oiData.Change1h),
		EffortLabel3m:     classifyEffortResult(computeEffortResult(priceChange3m, intradayData, oiData.Change5m)),
		EffortLabel15m:    classifyEffortResult(computeEffortResult(priceChange15m, intraday15m, oiData.Change15m)),
		EffortLabel1h:     classifyEffortResult(computeEffortResult(priceChange1h, intraday1h, oiData.Change1h)),
	}, nil
}

// computeEffortResult 计算价量+OI协同效率
// priceChangePercent: 该时间框架的价格百分比变化 (正负)；
// intraday: 对应的短周期数据(含 VolumeSpikeRatio)；
// oiChange: 对应时间尺度的 OI 变化率（例如 Change5m）
// 返回值：效率比 = 价格变化百分比 / (量能放大倍数 * (1 + |OI变化率|))，若分母为0返回0。
func computeEffortResult(priceChangePercent float64, intraday *IntradayData, oiChange float64) float64 {
	if intraday == nil {
		return 0
	}
	vs := intraday.VolumeSpikeRatio
	if vs <= 0 {
		return 0
	}
	denom := vs * (1 + math.Abs(oiChange))
	if denom == 0 {
		return 0
	}
	return priceChangePercent / denom
}

// classifyEffortResult 根据效率比返回标签
// >0.7 高效推进；0.3-0.7 正常；<0.3 低效/潜在吸筹或分配；若为负且绝对值大说明反向压力
func classifyEffortResult(r float64) string {
	switch {
	case r > 0.9:
		return "极高效率"
	case r > 0.7:
		return "高效率"
	case r > 0.3:
		return "正常"
	case r > 0:
		return "低效率"
	case r <= 0 && r > -0.3:
		return "反向轻压"
	case r <= -0.3 && r > -0.7:
		return "反向压力"
	default:
		return "强反向压力"
	}
}

// calculateEMA 计算EMA
func calculateEMA(klines []Kline, period int) float64 {
	if len(klines) < period {
		return 0
	}

	// 计算SMA作为初始EMA
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += klines[i].Close
	}
	ema := sum / float64(period)

	// 计算EMA
	multiplier := 2.0 / float64(period+1)
	for i := period; i < len(klines); i++ {
		ema = (klines[i].Close-ema)*multiplier + ema
	}

	return ema
}

// calculateEMAOfDIF 计算DIF序列的EMA（即DEA信号线）
func calculateEMAOfDIF(difSeries []float64, signalPeriod int) float64 {
	if len(difSeries) < signalPeriod {
		return 0
	}

	// 计算前signalPeriod个DIF值的SMA作为初始EMA
	sum := 0.0
	for i := 0; i < signalPeriod; i++ {
		sum += difSeries[i]
	}
	ema := sum / float64(signalPeriod)

	// 计算后续的EMA值
	multiplier := 2.0 / float64(signalPeriod+1)
	for i := signalPeriod; i < len(difSeries); i++ {
		ema = (difSeries[i]-ema)*multiplier + ema
	}

	return ema
}

// buildDIFSeries 构建DIF值序列
func buildDIFSeries(klines []Kline, shortPeriod, longPeriod int) []float64 {
	var difSeries []float64
	// 从第 longPeriod 根K线开始，才能计算出有效的EMA(longPeriod)
	for i := longPeriod - 1; i < len(klines); i++ {
		// 截取从开始到当前K线的子切片计算EMA
		subKlines := klines[:i+1]
		emaS := calculateEMA(subKlines, shortPeriod)
		emaL := calculateEMA(subKlines, longPeriod)
		difSeries = append(difSeries, emaS-emaL)
	}
	return difSeries
}

// calculateMACD 计算MACD指标的正确实现
// 参数: klines - K线数据切片, shortPeriod - 短期EMA周期(如12), longPeriod - 长期EMA周期(如26), signalPeriod - 信号线周期(如9)
// 返回值: dif - 快线, dea - 慢线(信号线), histogram - 柱状值
func calculateMACD(klines []Kline, shortPeriod, longPeriod, signalPeriod int) (float64, float64, float64) {
	// 1. 数据长度检查
	totalPeriod := longPeriod
	if shortPeriod > longPeriod {
		totalPeriod = shortPeriod
	}
	if len(klines) < totalPeriod {
		return 0, 0, 0
	}

	// 2. 计算DIF = EMA(close, short) - EMA(close, long)
	emaShort := calculateEMA(klines, shortPeriod)
	emaLong := calculateEMA(klines, longPeriod)
	dif := emaShort - emaLong

	// 3. 关键：需要先构建历史的DIF值序列，才能计算DEA
	// 获取从开始到当前的所有DIF值（需要一个辅助函数）
	difSeries := buildDIFSeries(klines, shortPeriod, longPeriod)
	if len(difSeries) < signalPeriod {
		return dif, 0, 0 // 如果DIF序列长度不足，无法计算有效的DEA
	}

	// 4. 计算DEA = EMA(DIF序列, signalPeriod)
	dea := calculateEMAOfDIF(difSeries, signalPeriod)

	// 5. 计算MACD柱状图 (Histogram)
	histogram := dif - dea

	// return dif, dea, histogram  （快线） （慢线）（柱状图）
	return dif, dea, histogram
}

// calculateRSI 计算RSI
func calculateRSI(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	gains := 0.0
	losses := 0.0

	// 计算初始平均涨跌幅
	for i := 1; i <= period; i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			gains += change
		} else {
			losses += -change
		}
	}

	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)

	// 使用Wilder平滑方法计算后续RSI
	for i := period + 1; i < len(klines); i++ {
		change := klines[i].Close - klines[i-1].Close
		if change > 0 {
			avgGain = (avgGain*float64(period-1) + change) / float64(period)
			avgLoss = (avgLoss * float64(period-1)) / float64(period)
		} else {
			avgGain = (avgGain * float64(period-1)) / float64(period)
			avgLoss = (avgLoss*float64(period-1) + (-change)) / float64(period)
		}
	}

	if avgLoss == 0 {
		return 100
	}

	rs := avgGain / avgLoss
	rsi := 100 - (100 / (1 + rs))

	return rsi
}

// calculateATR 计算ATR
func calculateATR(klines []Kline, period int) float64 {
	if len(klines) <= period {
		return 0
	}

	trs := make([]float64, len(klines))
	for i := 1; i < len(klines); i++ {
		high := klines[i].High
		low := klines[i].Low
		prevClose := klines[i-1].Close

		tr1 := high - low
		tr2 := math.Abs(high - prevClose)
		tr3 := math.Abs(low - prevClose)

		trs[i] = math.Max(tr1, math.Max(tr2, tr3))
	}

	// 计算初始ATR
	sum := 0.0
	for i := 1; i <= period; i++ {
		sum += trs[i]
	}
	atr := sum / float64(period)

	// Wilder平滑
	for i := period + 1; i < len(klines); i++ {
		atr = (atr*float64(period-1) + trs[i]) / float64(period)
	}

	return atr
}

// calculateIntradaySeries 计算日内系列数据
func calculateIntradaySeries(klines []Kline) *IntradayData {
	data := &IntradayData{
		MidPrices:       make([]float64, 0, 10),
		EMA20Values:     make([]float64, 0, 10),
		MACDValues10208: make([]float64, 0, 10),
		MACDValues12269: make([]float64, 0, 10),
		RSI7Values:      make([]float64, 0, 10),
		RSI9Values:      make([]float64, 0, 10),
		RSI10Values:     make([]float64, 0, 10),
		RSI14Values:     make([]float64, 0, 10),
		VolumeValues:    make([]float64, 0, 10),
	}
	// 计算ATR
	data.ATR6 = calculateATR(klines, 6)
	data.ATR10 = calculateATR(klines, 10)
	data.ATR12 = calculateATR(klines, 12)
	data.ATR14 = calculateATR(klines, 14)

	// 获取最近10个数据点
	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		data.MidPrices = append(data.MidPrices, klines[i].Close)
		data.VolumeValues = append(data.VolumeValues, klines[i].Volume)

		// 计算每个点的EMA20
		if i >= 19 {
			ema20 := calculateEMA(klines[:i+1], 20)
			data.EMA20Values = append(data.EMA20Values, ema20)
		}

		// 计算每个点的MACD
		if i >= 25 {
			dif, _, _ := calculateMACD(klines[:i+1], 10, 20, 8)
			macd := dif
			data.MACDValues10208 = append(data.MACDValues10208, macd)
		}
		// 计算每个点的MACD
		if i >= 25 {
			dif, _, _ := calculateMACD(klines[:i+1], 12, 26, 9)
			macd := dif
			data.MACDValues12269 = append(data.MACDValues12269, macd)
		}

		// 计算每个点的RSI
		if i >= 7 {
			rsi7 := calculateRSI(klines[:i+1], 7)
			data.RSI7Values = append(data.RSI7Values, rsi7)
		}
		if i >= 9 {
			rsi9 := calculateRSI(klines[:i+1], 9)
			data.RSI9Values = append(data.RSI9Values, rsi9)
		}
		if i >= 10 {
			rsi10 := calculateRSI(klines[:i+1], 10)
			data.RSI10Values = append(data.RSI10Values, rsi10)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
		}
	}

	// 量能统计：最近一个点与之前的平均比较
	if len(data.VolumeValues) > 1 {
		var sum float64
		for i := 0; i < len(data.VolumeValues)-1; i++ {
			sum += data.VolumeValues[i]
		}
		denom := float64(len(data.VolumeValues) - 1)
		if denom > 0 {
			data.VolumeAverage = sum / denom
			if data.VolumeAverage > 0 {
				data.VolumeSpikeRatio = data.VolumeValues[len(data.VolumeValues)-1] / data.VolumeAverage
			}
		}
	}

	return data
}

// calculateLongerTermData 计算长期数据
func calculateLongerTermData(klines []Kline) *LongerTermData {
	data := &LongerTermData{
		MACDValues142810: make([]float64, 0, 10),
		MACDValues12269:  make([]float64, 0, 10),
		RSI14Values:      make([]float64, 0, 10),
		RSI21Values:      make([]float64, 0, 10),
	}

	// 计算EMA
	data.EMA20 = calculateEMA(klines, 20)
	data.EMA50 = calculateEMA(klines, 50)

	// 计算ATR
	data.ATR3 = calculateATR(klines, 3)
	data.ATR10 = calculateATR(klines, 10)
	data.ATR12 = calculateATR(klines, 12)
	data.ATR14 = calculateATR(klines, 14)

	// 计算成交量
	if len(klines) > 0 {
		data.CurrentVolume = klines[len(klines)-1].Volume
		// 计算平均成交量
		sum := 0.0
		for _, k := range klines {
			sum += k.Volume
		}
		data.AverageVolume = sum / float64(len(klines))
	}

	// 计算MACD和RSI序列
	start := len(klines) - 10
	if start < 0 {
		start = 0
	}

	for i := start; i < len(klines); i++ {
		if i >= 25 {
			dif, _, _ := calculateMACD(klines[:i+1], 14, 28, 10)
			macd := dif
			data.MACDValues142810 = append(data.MACDValues142810, macd)
		}
		if i >= 25 {
			dif, _, _ := calculateMACD(klines[:i+1], 12, 26, 9)
			macd := dif
			data.MACDValues12269 = append(data.MACDValues12269, macd)
		}
		if i >= 14 {
			rsi14 := calculateRSI(klines[:i+1], 14)
			data.RSI14Values = append(data.RSI14Values, rsi14)
		}
		if i >= 21 {
			rsi21 := calculateRSI(klines[:i+1], 21)
			data.RSI21Values = append(data.RSI21Values, rsi21)
		}
	}

	return data
}

// getOpenInterestData 获取OI数据
func getOpenInterestData(symbol string) (*OIData, error) {
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/openInterest?symbol=%s", symbol)

	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		OpenInterest string `json:"openInterest"`
		Symbol       string `json:"symbol"`
		Time         int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	oi, parseErr := strconv.ParseFloat(result.OpenInterest, 64)
	if parseErr != nil {
		return nil, fmt.Errorf("parse openInterest failed: %w", parseErr)
	}

	// --- 构建历史序列与变化率 ---
	// 说明：当前实现没有本地持久化，此处仅演示：
	// 从全局map(按symbol)缓存一个短期序列（例如最近 288 * 5m ≈ 1天），
	// 并基于不同倍率聚合得到 5m/15m/1h/4h/1d 的抽样点。
	series := updateOISeriesCache(symbol, oi)
	// 聚合函数：给出不同窗口的最新两个点的变化率
	calcChange := func(slice []float64) float64 {
		if len(slice) < 2 {
			return 0
		}
		prev := slice[len(slice)-2]
		curr := slice[len(slice)-1]
		if prev == 0 {
			return 0
		}
		return (curr - prev) / prev
	}

	change5m := calcChange(series.fiveMins)
	change15m := calcChange(series.fifteenMins)
	change1h := calcChange(series.oneHours)
	change4h := calcChange(series.fourHours)
	change1d := calcChange(series.oneDays)

	trendScore := (change5m + change15m + change1h + change4h + change1d) / 5.0

	return &OIData{
		Latest:     oi,
		Average:    oi * 0.999, // TODO: 可替换为真实滑动平均
		Series5m:   append([]float64(nil), series.fiveMins...),
		Series15m:  append([]float64(nil), series.fifteenMins...),
		Series1h:   append([]float64(nil), series.oneHours...),
		Series4h:   append([]float64(nil), series.fourHours...),
		Series1d:   append([]float64(nil), series.oneDays...),
		Change5m:   change5m,
		Change15m:  change15m,
		Change1h:   change1h,
		Change4h:   change4h,
		Change1d:   change1d,
		TrendScore: trendScore,
	}, nil
}

// --- OI 序列缓存结构与更新逻辑 ---
type oiSeries struct {
	fiveMins    []float64
	fifteenMins []float64
	oneHours    []float64
	fourHours   []float64
	oneDays     []float64
	last5m      time.Time
	last15m     time.Time
	last1h      time.Time
	last4h      time.Time
	last1d      time.Time
}

var oiSeriesCache = struct {
	mu   sync.Mutex
	data map[string]*oiSeries
}{data: make(map[string]*oiSeries)}

func updateOISeriesCache(symbol string, oi float64) *oiSeries {
	now := time.Now()
	oiSeriesCache.mu.Lock()
	defer oiSeriesCache.mu.Unlock()

	s, ok := oiSeriesCache.data[symbol]
	if !ok {
		s = &oiSeries{}
		// 强制初始添加，避免空 slice
		s.fiveMins = append(s.fiveMins, oi)
		s.fifteenMins = append(s.fifteenMins, oi)
		s.oneHours = append(s.oneHours, oi)
		s.fourHours = append(s.fourHours, oi)
		s.oneDays = append(s.oneDays, oi)
		s.last5m = now
		s.last15m = now
		s.last1h = now
		s.last4h = now
		s.last1d = now
		oiSeriesCache.data[symbol] = s
		return s
	}

	// 5m 序列
	if now.Sub(s.last5m) >= 5*time.Minute {
		s.fiveMins = append(s.fiveMins, oi)
		s.last5m = now
	}
	// 15m 序列
	if now.Sub(s.last15m) >= 15*time.Minute {
		s.fifteenMins = append(s.fifteenMins, oi)
		s.last15m = now
	}
	// 1h 序列
	if now.Sub(s.last1h) >= time.Hour {
		s.oneHours = append(s.oneHours, oi)
		s.last1h = now
	}
	// 4h 序列
	if now.Sub(s.last4h) >= 4*time.Hour {
		s.fourHours = append(s.fourHours, oi)
		s.last4h = now
	}
	// 1d 序列
	if now.Sub(s.last1d) >= 24*time.Hour {
		s.oneDays = append(s.oneDays, oi)
		s.last1d = now
	}

	// 截断长度避免无限增长（保留最近300个点即可）
	trim := func(arr []float64) []float64 {
		if len(arr) > 300 {
			return arr[len(arr)-300:]
		}
		return arr
	}
	s.fiveMins = trim(s.fiveMins)
	s.fifteenMins = trim(s.fifteenMins)
	s.oneHours = trim(s.oneHours)
	s.fourHours = trim(s.fourHours)
	s.oneDays = trim(s.oneDays)

	return s
}

// getFundingRate 获取资金费率
func getFundingRate(symbol string) (float64, error) {
	url := fmt.Sprintf("https://fapi.binance.com/fapi/v1/premiumIndex?symbol=%s", symbol)

	resp, err := http.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result struct {
		Symbol          string `json:"symbol"`
		MarkPrice       string `json:"markPrice"`
		IndexPrice      string `json:"indexPrice"`
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
		InterestRate    string `json:"interestRate"`
		Time            int64  `json:"time"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}

	rate, _ := strconv.ParseFloat(result.LastFundingRate, 64)
	// 旧代码中的 err 检查已无意义（变量被覆盖），改为显式错误处理
	// 如果需要严格处理，可改为：rateParsed, parseErr := strconv.ParseFloat(...)
	// 这里保留简单解析，失败返回 0
	return rate, nil
}

// Format 格式化输出市场数据
func Format(data *Data) string {
	var sb strings.Builder

	// 基础价格信息（包含新增的时间框架价格变化）
	sb.WriteString(fmt.Sprintf("当前价格 = %.2f, 20期EMA = %.3f, MACD = %.3f, 7期RSI = %.3f\n\n",
		data.CurrentPrice, data.CurrentEMA20, data.CurrentMACD, data.CurrentRSI7))
	sb.WriteString(fmt.Sprintf("价格变化: 3分钟=%.2f%%, 15分钟=%.2f%%, 1小时=%.2f%%, 4小时=%.2f%%, 1天=%.2f%%\n",
		data.PriceChange3m, data.PriceChange15m, data.PriceChange1h, data.PriceChange4h, data.PriceChange1d))
	sb.WriteString(fmt.Sprintf("协同效率: 3m=%.3f(%s), 15m=%.3f(%s), 1h=%.3f(%s)\n\n",
		data.EffortResult3m, data.EffortLabel3m,
		data.EffortResult15m, data.EffortLabel15m,
		data.EffortResult1h, data.EffortLabel1h))

	// 持仓量和资金费率
	sb.WriteString(fmt.Sprintf("合约市场数据（%s）:\n\n", data.Symbol))
	if data.OpenInterest != nil {
		sb.WriteString(fmt.Sprintf("持仓量: 最新=%.2f, 平均=%.2f\n",
			data.OpenInterest.Latest, data.OpenInterest.Average))
		// 新增：OI变化率与趋势
		sb.WriteString(fmt.Sprintf("OI变化率: 5m=%.3f%%, 15m=%.3f%%, 1h=%.3f%%, 4h=%.3f%%, 1d=%.3f%%\n",
			data.OpenInterest.Change5m*100,
			data.OpenInterest.Change15m*100,
			data.OpenInterest.Change1h*100,
			data.OpenInterest.Change4h*100,
			data.OpenInterest.Change1d*100))
		sb.WriteString(fmt.Sprintf("OI趋势评分: %.3f\n\n", data.OpenInterest.TrendScore))
	}
	sb.WriteString(fmt.Sprintf("资金费率: %.2e\n\n", data.FundingRate))

	// 3分钟数据展示（原有）
	if data.IntradaySeries != nil {
		sb.WriteString("日内数据（3分钟周期，从旧到新）:\n\n")
		sb.WriteString(fmt.Sprintf("10期ATR: %.3f \n\n", data.IntradaySeries.ATR10))
		if len(data.IntradaySeries.VolumeValues) > 0 {
			sb.WriteString(fmt.Sprintf("成交量序列: %s\n", formatFloatSlice(data.IntradaySeries.VolumeValues)))
			sb.WriteString(fmt.Sprintf("平均成交量: %.2f, 量能放大倍数: %.2f\n\n", data.IntradaySeries.VolumeAverage, data.IntradaySeries.VolumeSpikeRatio))
		}
		if len(data.IntradaySeries.MidPrices) > 0 {
			sb.WriteString(fmt.Sprintf("中间价: %s\n\n", formatFloatSlice(data.IntradaySeries.MidPrices)))
		}
		if len(data.IntradaySeries.EMA20Values) > 0 {
			sb.WriteString(fmt.Sprintf("20期EMA指标: %s\n\n", formatFloatSlice(data.IntradaySeries.EMA20Values)))
		}
		if len(data.IntradaySeries.MACDValues10208) > 0 {
			sb.WriteString(fmt.Sprintf("MACD(10,20,8)指标: %s\n\n", formatFloatSlice(data.IntradaySeries.MACDValues10208)))
		}
		if len(data.IntradaySeries.RSI10Values) > 0 {
			sb.WriteString(fmt.Sprintf("10期RSI指标: %s\n\n", formatFloatSlice(data.IntradaySeries.RSI10Values)))
		}
		if len(data.IntradaySeries.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("14期RSI指标: %s\n\n", formatFloatSlice(data.IntradaySeries.RSI14Values)))
		}
	}

	// 新增：15分钟数据展示
	if data.Intraday15m != nil {
		sb.WriteString("日内数据（15分钟周期，从旧到新）:\n\n")
		sb.WriteString(fmt.Sprintf("12期ATR: %.3f \n\n", data.Intraday15m.ATR12))
		if len(data.Intraday15m.MidPrices) > 0 {
			sb.WriteString(fmt.Sprintf("中间价: %s\n\n", formatFloatSlice(data.Intraday15m.MidPrices)))
		}
		if len(data.Intraday15m.EMA20Values) > 0 {
			sb.WriteString(fmt.Sprintf("20期EMA指标: %s\n\n", formatFloatSlice(data.Intraday15m.EMA20Values)))
		}
		if len(data.Intraday15m.MACDValues12269) > 0 {
			sb.WriteString(fmt.Sprintf("MACD(12,26,9)指标: %s\n\n", formatFloatSlice(data.Intraday15m.MACDValues12269)))
		}
		if len(data.Intraday15m.RSI7Values) > 0 {
			sb.WriteString(fmt.Sprintf("7期RSI指标: %s\n\n", formatFloatSlice(data.Intraday15m.RSI7Values)))
		}
		if len(data.Intraday15m.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("14期RSI指标: %s\n\n", formatFloatSlice(data.Intraday15m.RSI14Values)))
		}
	}

	// 新增：1小时数据展示
	if data.Intraday1h != nil {
		sb.WriteString("日内数据（1小时周期，从旧到新）:\n\n")
		sb.WriteString(fmt.Sprintf("6期ATR: %.3f vs 14期ATR: %.3f\n\n", data.Intraday1h.ATR6, data.Intraday1h.ATR14))

		if len(data.Intraday1h.MidPrices) > 0 {
			sb.WriteString(fmt.Sprintf("中间价: %s\n\n", formatFloatSlice(data.Intraday1h.MidPrices)))
		}
		if len(data.Intraday1h.EMA20Values) > 0 {
			sb.WriteString(fmt.Sprintf("20期EMA指标: %s\n\n", formatFloatSlice(data.Intraday1h.EMA20Values)))
		}
		if len(data.Intraday1h.MACDValues12269) > 0 {
			sb.WriteString(fmt.Sprintf("MACD(12,26,9)指标: %s\n\n", formatFloatSlice(data.Intraday1h.MACDValues12269)))
		}
		if len(data.Intraday1h.RSI9Values) > 0 {
			sb.WriteString(fmt.Sprintf("9期RSI指标: %s\n\n", formatFloatSlice(data.Intraday1h.RSI9Values)))
		}
		if len(data.Intraday1h.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("14期RSI指标: %s\n\n", formatFloatSlice(data.Intraday1h.RSI14Values)))
		}
	}

	// 4小时数据展示（原有）
	if data.LongerTermContext != nil {
		sb.WriteString("长期数据（4小时周期）:\n\n")
		sb.WriteString(fmt.Sprintf("20期EMA: %.3f vs 50期EMA: %.3f\n\n",
			data.LongerTermContext.EMA20, data.LongerTermContext.EMA50))
		sb.WriteString(fmt.Sprintf("3期ATR: %.3f vs 14期ATR: %.3f\n\n",
			data.LongerTermContext.ATR3, data.LongerTermContext.ATR14))
		sb.WriteString(fmt.Sprintf("当前成交量: %.3f vs 平均成交量: %.3f\n\n",
			data.LongerTermContext.CurrentVolume, data.LongerTermContext.AverageVolume))
		if len(data.LongerTermContext.MACDValues142810) > 0 {
			sb.WriteString(fmt.Sprintf("MACD(14,28,10)指标: %s\n\n", formatFloatSlice(data.LongerTermContext.MACDValues142810)))
		}
		if len(data.LongerTermContext.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("14期RSI指标: %s\n\n", formatFloatSlice(data.LongerTermContext.RSI14Values)))
		}
		if len(data.LongerTermContext.RSI21Values) > 0 {
			sb.WriteString(fmt.Sprintf("21期RSI指标: %s\n\n", formatFloatSlice(data.LongerTermContext.RSI21Values)))
		}
	}

	// 新增：1天数据展示
	if data.LongerTerm1d != nil {
		sb.WriteString("长期数据（1天周期）:\n\n")
		sb.WriteString(fmt.Sprintf("20期EMA: %.3f vs 50期EMA: %.3f\n\n",
			data.LongerTerm1d.EMA20, data.LongerTerm1d.EMA50))
		sb.WriteString(fmt.Sprintf("3期ATR: %.3f vs 14期ATR: %.3f\n\n",
			data.LongerTerm1d.ATR3, data.LongerTerm1d.ATR14))
		sb.WriteString(fmt.Sprintf("当前成交量: %.3f vs 平均成交量: %.3f\n\n",
			data.LongerTerm1d.CurrentVolume, data.LongerTerm1d.AverageVolume))
		if len(data.LongerTerm1d.MACDValues12269) > 0 {
			sb.WriteString(fmt.Sprintf("MACD(12,26,9)指标: %s\n\n", formatFloatSlice(data.LongerTerm1d.MACDValues12269)))
		}
		if len(data.LongerTerm1d.RSI14Values) > 0 {
			sb.WriteString(fmt.Sprintf("14期RSI指标: %s\n\n", formatFloatSlice(data.LongerTerm1d.RSI14Values)))
		}
	}

	return sb.String()
}

// formatFloatSlice 格式化float64切片为字符串
func formatFloatSlice(values []float64) string {
	strValues := make([]string, len(values))
	for i, v := range values {
		strValues[i] = fmt.Sprintf("%.3f", v)
	}
	return "[" + strings.Join(strValues, ", ") + "]"
}

// Normalize 标准化symbol,确保是USDT交易对
func Normalize(symbol string) string {
	symbol = strings.ToUpper(symbol)
	if strings.HasSuffix(symbol, "USDT") {
		return symbol
	}
	return symbol + "USDT"
}

// parseFloat 解析float值
func parseFloat(v interface{}) (float64, error) {
	switch val := v.(type) {
	case string:
		return strconv.ParseFloat(val, 64)
	case float64:
		return val, nil
	case int:
		return float64(val), nil
	case int64:
		return float64(val), nil
	default:
		return 0, fmt.Errorf("unsupported type: %T", v)
	}
}
