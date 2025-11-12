package market

import "time"

// Data 市场数据结构
type Data struct {
	Symbol            string
	CurrentPrice      float64
	PriceChange3m     float64 // 新增：最近一个3m与前一个3m的价格变化百分比
	PriceChange1h     float64 // 1小时价格变化百分比
	PriceChange4h     float64 // 4小时价格变化百分比
	PriceChange15m    float64 // 新增：15分钟价格变化百分比
	PriceChange1d     float64 // 新增：1天价格变化百分比
	CurrentEMA20      float64
	CurrentMACD       float64
	CurrentRSI7       float64
	OpenInterest      *OIData
	FundingRate       float64
	IntradaySeries    *IntradayData   // 3分钟数据
	Intraday15m       *IntradayData   // 新增：15分钟数据
	Intraday1h        *IntradayData   // 新增：1小时数据
	LongerTermContext *LongerTermData // 4小时数据
	LongerTerm1d      *LongerTermData // 新增：1天数据

	// Effort vs Result 指标 (价量 + OI 共振效率) 越高代表价格推进效率高
	EffortResult3m  float64
	EffortResult15m float64
	EffortResult1h  float64
	// 解释标签 (高效/低效/背离)，便于直接输出
	EffortLabel3m  string
	EffortLabel15m string
	EffortLabel1h  string
}

// OIData Open Interest数据
type OIData struct {
	Latest  float64
	Average float64
	// 历史序列（不同周期）
	Series5m  []float64
	Series15m []float64
	Series1h  []float64
	Series4h  []float64
	Series1d  []float64

	// 变化率（相邻最新两点的百分比变化）
	Change5m  float64
	Change15m float64
	Change1h  float64
	Change4h  float64
	Change1d  float64

	// 趋势评分（简单地取各周期变化率的平均，后续可替换为线性回归斜率加权）
	TrendScore float64
}

// IntradayData 日内数据(3分钟,15,1小时)
type IntradayData struct {
	ATR6  float64
	ATR10 float64
	ATR12 float64
	ATR14 float64

	MidPrices   []float64
	EMA20Values []float64

	MACDValues10208 []float64
	MACDValues12269 []float64

	RSI7Values  []float64
	RSI9Values  []float64
	RSI10Values []float64
	RSI14Values []float64

	// 新增：成交量序列与量能指标
	VolumeValues     []float64 // 最近10个点的成交量
	VolumeAverage    float64   // 最近10个点平均成交量
	VolumeSpikeRatio float64   // 最新成交量 / 之前N(默认为9)个平均成交量
}

// LongerTermData 长期数据(4小时时间框架1天)
type LongerTermData struct {
	EMA20 float64
	EMA50 float64

	ATR3  float64
	ATR10 float64
	ATR12 float64
	ATR14 float64

	CurrentVolume float64
	AverageVolume float64

	MACDValues142810 []float64
	MACDValues12269  []float64
	RSI14Values      []float64
	RSI21Values      []float64
}

// Binance API 响应结构
type ExchangeInfo struct {
	Symbols []SymbolInfo `json:"symbols"`
}

type SymbolInfo struct {
	Symbol            string `json:"symbol"`
	Status            string `json:"status"`
	BaseAsset         string `json:"baseAsset"`
	QuoteAsset        string `json:"quoteAsset"`
	ContractType      string `json:"contractType"`
	PricePrecision    int    `json:"pricePrecision"`
	QuantityPrecision int    `json:"quantityPrecision"`
}

type Kline struct {
	OpenTime            int64   `json:"openTime"`
	Open                float64 `json:"open"`
	High                float64 `json:"high"`
	Low                 float64 `json:"low"`
	Close               float64 `json:"close"`
	Volume              float64 `json:"volume"`
	CloseTime           int64   `json:"closeTime"`
	QuoteVolume         float64 `json:"quoteVolume"`
	Trades              int     `json:"trades"`
	TakerBuyBaseVolume  float64 `json:"takerBuyBaseVolume"`
	TakerBuyQuoteVolume float64 `json:"takerBuyQuoteVolume"`
}

type KlineResponse []interface{}

type PriceTicker struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
}

type Ticker24hr struct {
	Symbol             string `json:"symbol"`
	PriceChange        string `json:"priceChange"`
	PriceChangePercent string `json:"priceChangePercent"`
	Volume             string `json:"volume"`
	QuoteVolume        string `json:"quoteVolume"`
}

// 特征数据结构
type SymbolFeatures struct {
	Symbol           string    `json:"symbol"`
	Timestamp        time.Time `json:"timestamp"`
	Price            float64   `json:"price"`
	PriceChange15Min float64   `json:"price_change_15min"`
	PriceChange1H    float64   `json:"price_change_1h"`
	PriceChange4H    float64   `json:"price_change_4h"`
	Volume           float64   `json:"volume"`
	VolumeRatio5     float64   `json:"volume_ratio_5"`
	VolumeRatio20    float64   `json:"volume_ratio_20"`
	VolumeTrend      float64   `json:"volume_trend"`
	RSI14            float64   `json:"rsi_14"`
	SMA5             float64   `json:"sma_5"`
	SMA10            float64   `json:"sma_10"`
	SMA20            float64   `json:"sma_20"`
	HighLowRatio     float64   `json:"high_low_ratio"`
	Volatility20     float64   `json:"volatility_20"`
	PositionInRange  float64   `json:"position_in_range"`
}

// 警报数据结构
type Alert struct {
	Type      string    `json:"type"`
	Symbol    string    `json:"symbol"`
	Value     float64   `json:"value"`
	Threshold float64   `json:"threshold"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

type Config struct {
	AlertThresholds AlertThresholds `json:"alert_thresholds"`
	UpdateInterval  int             `json:"update_interval"` // seconds
	CleanupConfig   CleanupConfig   `json:"cleanup_config"`
}

type AlertThresholds struct {
	VolumeSpike      float64 `json:"volume_spike"`
	PriceChange15Min float64 `json:"price_change_15min"`
	VolumeTrend      float64 `json:"volume_trend"`
	RSIOverbought    float64 `json:"rsi_overbought"`
	RSIOversold      float64 `json:"rsi_oversold"`
}
type CleanupConfig struct {
	InactiveTimeout   time.Duration `json:"inactive_timeout"`    // 不活跃超时时间
	MinScoreThreshold float64       `json:"min_score_threshold"` // 最低评分阈值
	NoAlertTimeout    time.Duration `json:"no_alert_timeout"`    // 无警报超时时间
	CheckInterval     time.Duration `json:"check_interval"`      // 检查间隔
}

var config = Config{
	AlertThresholds: AlertThresholds{
		VolumeSpike:      3.0,
		PriceChange15Min: 0.05,
		VolumeTrend:      2.0,
		RSIOverbought:    70,
		RSIOversold:      30,
	},
	CleanupConfig: CleanupConfig{
		InactiveTimeout:   30 * time.Minute,
		MinScoreThreshold: 15.0,
		NoAlertTimeout:    20 * time.Minute,
		CheckInterval:     5 * time.Minute,
	},
	UpdateInterval: 60, // 1 minute
}
