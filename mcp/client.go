package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Provider AI提供商类型
type Provider string

const (
	ProviderDeepSeek Provider = "deepseek"
	ProviderQwen     Provider = "qwen"
	ProviderCustom   Provider = "custom"
	// ProviderSiliconFlow 可选：用于标识 SiliconFlow（若使用 SetCustomAPI 也能工作，这里只是更清晰）
	ProviderSiliconFlow Provider = "siliconflow"
)

// Client AI API配置
type Client struct {
	Provider   Provider
	APIKey     string
	APIKeys    []string // 支持多密钥；启动时随机选择一个
	BaseURL    string
	Model      string
	Timeout    time.Duration
	UseFullURL bool // 是否使用完整URL（不添加/chat/completions）
	MaxTokens  int  // AI响应的最大token数
	// PersistRemovedKey 当某个密钥被判定余额不足而移除时回调，负责持久化到数据库
	PersistRemovedKey func(provider Provider, removedKey string, remaining []string) error
	// 如果后续需要缓存余额，可在这里加一个字段，例如 lastBalance string / lastBalanceAt time.Time
}

func New() *Client {
	// 从环境变量读取 MaxTokens，默认 2000
	maxTokens := 2000
	if envMaxTokens := os.Getenv("AI_MAX_TOKENS"); envMaxTokens != "" {
		if parsed, err := strconv.Atoi(envMaxTokens); err == nil && parsed > 0 {
			maxTokens = parsed
			log.Printf("🔧 [MCP] 使用环境变量 AI_MAX_TOKENS: %d", maxTokens)
		} else {
			log.Printf("⚠️  [MCP] 环境变量 AI_MAX_TOKENS 无效 (%s)，使用默认值: %d", envMaxTokens, maxTokens)
		}
	}

	// 默认配置
	return &Client{
		Provider:  ProviderDeepSeek,
		BaseURL:   "https://api.deepseek.com/v1",
		Model:     "deepseek-chat",
		Timeout:   120 * time.Second, // 增加到120秒，因为AI需要分析大量数据
		MaxTokens: maxTokens,
	}
}

// SetDeepSeekAPIKey 设置DeepSeek API密钥
// customURL 为空时使用默认URL，customModel 为空时使用默认模型
func (client *Client) SetDeepSeekAPIKey(apiKey string, customURL string, customModel string) {
	client.Provider = ProviderDeepSeek
	client.setAPIKeysFromString(apiKey)
	if customURL != "" {
		client.BaseURL = customURL
		log.Printf("🔧 [MCP] DeepSeek 使用自定义 BaseURL: %s", customURL)
	} else {
		client.BaseURL = "https://api.deepseek.com/v1"
		log.Printf("🔧 [MCP] DeepSeek 使用默认 BaseURL: %s", client.BaseURL)
	}
	if customModel != "" {
		client.Model = customModel
		log.Printf("🔧 [MCP] DeepSeek 使用自定义 Model: %s", customModel)
	} else {
		client.Model = "deepseek-chat"
		log.Printf("🔧 [MCP] DeepSeek 使用默认 Model: %s", client.Model)
	}
	client.logActiveKey("DeepSeek")
}

// SetQwenAPIKey 设置阿里云Qwen API密钥
// customURL 为空时使用默认URL，customModel 为空时使用默认模型
func (client *Client) SetQwenAPIKey(apiKey string, customURL string, customModel string) {
	client.Provider = ProviderQwen
	client.setAPIKeysFromString(apiKey)
	if customURL != "" {
		client.BaseURL = customURL
		log.Printf("🔧 [MCP] Qwen 使用自定义 BaseURL: %s", customURL)
	} else {
		client.BaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
		log.Printf("🔧 [MCP] Qwen 使用默认 BaseURL: %s", client.BaseURL)
	}
	if customModel != "" {
		client.Model = customModel
		log.Printf("🔧 [MCP] Qwen 使用自定义 Model: %s", customModel)
	} else {
		client.Model = "qwen3-max"
		log.Printf("🔧 [MCP] Qwen 使用默认 Model: %s", client.Model)
	}
	client.logActiveKey("Qwen")
}

// SetCustomAPI 设置自定义OpenAI兼容API
func (client *Client) SetCustomAPI(apiURL, apiKey, modelName string) {
	client.Provider = ProviderCustom
	client.setAPIKeysFromString(apiKey)

	// 检查URL是否以#结尾，如果是则使用完整URL（不添加/chat/completions）
	if strings.HasSuffix(apiURL, "#") {
		client.BaseURL = strings.TrimSuffix(apiURL, "#")
		client.UseFullURL = true
	} else {
		client.BaseURL = apiURL
		client.UseFullURL = false
	}

	client.Model = modelName
	client.Timeout = 120 * time.Second
}

// SetClient 设置完整的AI配置（高级用户）
func (client *Client) SetClient(Client Client) {
	if Client.Timeout == 0 {
		Client.Timeout = 30 * time.Second
	}
	client = &Client
}

// CallWithMessages 使用 system + user prompt 调用AI API（推荐）
func (client *Client) CallWithMessages(systemPrompt, userPrompt string) (string, error) {
	if client.APIKey == "" {
		return "", fmt.Errorf("AI API密钥未设置，请先调用 SetDeepSeekAPIKey() 或 SetQwenAPIKey()")
	}
	// 按需求：报错后不再重试（行情可能已变化）
	return client.callOnce(systemPrompt, userPrompt)
}

// callOnce 单次调用AI API（内部使用）
func (client *Client) callOnce(systemPrompt, userPrompt string) (string, error) {
	// 如果没有激活key，但有候选列表，则随机选择一个
	if len(client.APIKeys) > 0 { // 每次调用前都随机挑选一个，满足“每次调用随机使用其中一个”
		client.selectRandomKey()
	}

	// 打印当前 AI 配置
	log.Printf("📡 [MCP] AI 请求配置:")
	log.Printf("   Provider: %s", client.Provider)
	log.Printf("   BaseURL: %s", client.BaseURL)
	log.Printf("   Model: %s", client.Model)
	log.Printf("   UseFullURL: %v", client.UseFullURL)
	if len(client.APIKey) > 8 {
		log.Printf("   API Key: %s...%s", client.APIKey[:4], client.APIKey[len(client.APIKey)-4:])
	}

	// 如果是 SiliconFlow（通过域名判断，或 Provider 明确），查询账户余额便于日志与后续策略判定
	if isSiliconFlow(client) {
		if info, key, err := fetchSiliconFlowUserInfo(client); err == nil {
			log.Printf("💰 [MCP] SiliconFlow(%s) 账户余额: %s (totalBalance=%s, chargeBalance=%s)", key, info.Data.Balance, info.Data.TotalBalance, info.Data.ChargeBalance)
		} else {
			log.Printf("⚠️  [MCP] 获取 SiliconFlow 余额失败: %v", err)
		}
	}

	// 构建 messages 数组
	messages := []map[string]string{}

	// 如果有 system prompt，添加 system message
	if systemPrompt != "" {
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": systemPrompt,
		})
	}

	// 添加 user message
	messages = append(messages, map[string]string{
		"role":    "user",
		"content": userPrompt,
	})

	// 构建请求体
	requestBody := map[string]interface{}{
		"model":       client.Model,
		"messages":    messages,
		"temperature": 0.5, // 降低temperature以提高JSON格式稳定性
		"max_tokens":  client.MaxTokens,
	}

	// 注意：response_format 参数仅 OpenAI 支持，DeepSeek/Qwen 不支持
	// 我们通过强化 prompt 和后处理来确保 JSON 格式正确

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	// 创建HTTP请求
	var url string
	if client.UseFullURL {
		// 使用完整URL，不添加/chat/completions
		url = client.BaseURL
	} else {
		// 默认行为：添加/chat/completions
		url = fmt.Sprintf("%s/chat/completions", client.BaseURL)
	}
	log.Printf("📡 [MCP] 请求 URL: %s", url)

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// 根据不同的Provider设置认证方式
	switch client.Provider {
	case ProviderDeepSeek:
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", client.APIKey))
	case ProviderQwen:
		// 阿里云Qwen使用API-Key认证
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", client.APIKey))
		// 注意：如果使用的不是兼容模式，可能需要不同的认证方式
	default:
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", client.APIKey))
	}

	// 发送请求
	httpClient := &http.Client{Timeout: client.Timeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// 余额不足处理：删除当前key，不再重试
		bodyStr := string(body)
		if isInsufficientBalance(bodyStr) {
			removed := client.removeCurrentKey()
			if removed != "" {
				log.Printf("🧹 [MCP] 检测到余额不足，已移除当前API Key: %s", maskAPIKey(removed))
			}
		}
		return "", fmt.Errorf("API返回错误 (status %d): %s", resp.StatusCode, bodyStr)
	}

	// 解析响应
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析响应失败: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("API返回空响应")
	}

	return result.Choices[0].Message.Content, nil
}

// isRetryableError 判断错误是否可重试
func isRetryableError(err error) bool {
	errStr := err.Error()
	// 网络错误、超时、EOF等可以重试
	retryableErrors := []string{
		"EOF",
		"timeout",
		"connection reset",
		"connection refused",
		"temporary failure",
		"no such host",
		"stream error",   // HTTP/2 stream 错误
		"INTERNAL_ERROR", // 服务端内部错误
	}
	for _, retryable := range retryableErrors {
		if strings.Contains(errStr, retryable) {
			return true
		}
	}
	return false
}

// ---------------- 多Key 管理 ----------------

// setAPIKeysFromString 支持逗号/分号/空白/换行分隔的多Key输入
func (client *Client) setAPIKeysFromString(keys string) {
	// 分割
	sep := func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}
	parts := strings.FieldsFunc(strings.TrimSpace(keys), sep)
	uniq := make(map[string]struct{})
	client.APIKeys = client.APIKeys[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := uniq[p]; ok {
			continue
		}
		uniq[p] = struct{}{}
		client.APIKeys = append(client.APIKeys, p)
	}

	// 随机选择一个作为当前激活key（满足“每次启动随机使用其中的一个”）
	if len(client.APIKeys) > 0 {
		client.selectRandomKey()
	} else {
		client.APIKey = ""
	}
}

// selectRandomKey 从列表中随机选一个作为当前key
func (client *Client) selectRandomKey() {
	if len(client.APIKeys) == 0 {
		client.APIKey = ""
		return
	}
	// 使用时间种子
	rnd := time.Now().UnixNano()
	idx := int(rnd % int64(len(client.APIKeys)))
	client.APIKey = client.APIKeys[idx]
}

// removeCurrentKey 将当前key从候选列表删除，并清空当前key
func (client *Client) removeCurrentKey() string {
	if client.APIKey == "" {
		return ""
	}
	removed := client.APIKey
	// 过滤掉当前key
	filtered := make([]string, 0, len(client.APIKeys))
	for _, k := range client.APIKeys {
		if k != removed {
			filtered = append(filtered, k)
		}
	}
	client.APIKeys = filtered
	client.APIKey = ""
	// 如果还有剩余key，随机切换一个供后续使用
	if len(client.APIKeys) > 0 {
		client.selectRandomKey()
		client.logActiveKey("切换")
	}
	// 持久化回调（从外部写回数据库）
	if client.PersistRemovedKey != nil {
		if err := client.PersistRemovedKey(client.Provider, removed, client.APIKeys); err != nil {
			log.Printf("⚠️  [MCP] 持久化移除API Key失败: %v", err)
		} else {
			log.Printf("📝 [MCP] 已持久化移除的API Key，剩余数量=%d", len(client.APIKeys))
		}
	}
	return removed
}

// logActiveKey 打印当前激活的key（脱敏）
func (client *Client) logActiveKey(prefix string) {
	if len(client.APIKey) > 8 {
		log.Printf("🔧 [MCP] %s API Key: %s", prefix, maskAPIKey(client.APIKey))
	}
}

// isInsufficientBalance 判断响应文本是否为余额不足
func isInsufficientBalance(s string) bool {
	lower := strings.ToLower(s)
	if strings.Contains(lower, "balance is insufficient") || strings.Contains(lower, "insufficient balance") {
		return true
	}
	if strings.Contains(s, "余额不足") {
		return true
	}
	if strings.Contains(s, "Sorry, your account balance is insufficient") {
		return true
	}
	return false
}

// ---------------- SiliconFlow 用户信息支持 ----------------

// siliconFlowUserInfo 响应结构（仅映射当前需要的字段）
type siliconFlowUserInfo struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  bool   `json:"status"`
	Data    struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Balance       string `json:"balance"`
		ChargeBalance string `json:"chargeBalance"`
		TotalBalance  string `json:"totalBalance"`
		Email         string `json:"email"`
	} `json:"data"`
}

// isSiliconFlow 判断是否为 SiliconFlow（通过域名或 Provider）
func isSiliconFlow(c *Client) bool {
	return strings.Contains(c.BaseURL, "siliconflow.cn") || c.Provider == ProviderSiliconFlow
}

// fetchSiliconFlowUserInfo 调用 /user/info 获取余额
// 返回值依次为：账户信息、脱敏后的 API Key（用于日志）、错误
func fetchSiliconFlowUserInfo(c *Client) (*siliconFlowUserInfo, string, error) {
	// SiliconFlow 基础地址通常为 https://api.siliconflow.cn/v1
	// 其用户信息接口：GET /user/info （不需要 /v1 前缀再追加）
	// 若 BaseURL 末尾存在 /v1，需要向上一级取 /user/info；这里直接裁掉末尾的 /v1 以保证兼容。
	var url = "https://api.siliconflow.cn/v1/user/info"

	// 脱敏后的 API Key 供日志使用
	maskedKey := maskAPIKey(c.APIKey)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, maskedKey, fmt.Errorf("创建 SiliconFlow 用户信息请求失败: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.APIKey))
	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, maskedKey, fmt.Errorf("发送 SiliconFlow 用户信息请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, maskedKey, fmt.Errorf("读取 SiliconFlow 用户信息响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, maskedKey, fmt.Errorf("SiliconFlow 用户信息接口返回非200: %d %s", resp.StatusCode, string(body))
	}
	var info siliconFlowUserInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, maskedKey, fmt.Errorf("解析 SiliconFlow 用户信息 JSON 失败: %w", err)
	}
	if !info.Status || info.Code != 20000 {
		return &info, maskedKey, fmt.Errorf("SiliconFlow 用户信息返回异常 code=%d status=%v message=%s", info.Code, info.Status, info.Message)
	}
	return &info, maskedKey, nil
}

// maskAPIKey 对 API Key 进行简单脱敏，仅保留前后各4位
func maskAPIKey(key string) string {
	if len(key) <= 8 {
		if len(key) == 0 {
			return "(empty)"
		}
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}
