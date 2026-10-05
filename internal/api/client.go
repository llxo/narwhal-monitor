package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimitError 记录接口 429 限速及建议退避时长
type RateLimitError struct {
	StatusCode int
	RetryAfter time.Duration
	Message    string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("平台接口限速 %d: %s (建议冷却退避 %v)", e.StatusCode, e.Message, e.RetryAfter)
}

const narwhalBaseURL = "https://api.fuckip.me/api/v1"

// Client 负责与 Narwhal Cloud API 进行交互，保证极低的内存分配、连接复用与完善的鉴权支持
type Client struct {
	httpClient  *http.Client
	mu          sync.RWMutex
	apiKey      string
	token       string
	cachedPlans []PublicPlan
	cacheTime   time.Time
}

// NewClient 创建并配置复用连接池的低占用 HTTP 客户端
func NewClient(apiKey string) *Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableCompression: false,
	}

	return &Client{
		apiKey: strings.TrimSpace(apiKey),
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   12 * time.Second,
		},
	}
}

// SetAPIKey 动态更新 API Key
func (c *Client) SetAPIKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiKey = strings.TrimSpace(key)
}

// SetToken 动态更新 JWT 访问 Token
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = strings.TrimSpace(token)
}

// getAuthHeader 获取 Bearer 鉴权头
func (c *Client) getAuthHeader() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.apiKey != "" {
		return "Bearer " + c.apiKey
	}
	if c.token != "" {
		return "Bearer " + c.token
	}
	return ""
}

// HasAuth 检查当前是否配置了鉴权凭据
func (c *Client) HasAuth() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiKey != "" || c.token != ""
}

// Login 使用邮箱与密码登录获取 JWT Token
func (c *Client) Login(ctx context.Context, email, password string) (*LoginResult, error) {
	url := fmt.Sprintf("%s/auth/login", narwhalBaseURL)
	reqBody, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("创建登录请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "NarwhalMonitorBot/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送登录请求失败: %w", err)
	}
	defer resp.Body.Close()

	var res ApiResponse[LoginResult]
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("解析登录响应失败: %w", err)
	}

	if res.Code != 0 {
		return nil, fmt.Errorf("登录失败: code=%d msg=%s", res.Code, res.Msg)
	}

	c.SetToken(res.Data.Token)
	return &res.Data, nil
}

// GetMe 获取当前登录用户画像，验证 API Key 或 Token 是否有效
func (c *Client) GetMe(ctx context.Context) (*UserResponse, error) {
	url := fmt.Sprintf("%s/user/me", narwhalBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	c.injectHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("验证 API Key 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("鉴权失败 (状态码 %d): API Key 或 Token 无效", resp.StatusCode)
	}

	var res ApiResponse[UserResponse]
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("解析个人资料响应失败: %w", err)
	}

	if res.Code != 0 {
		return nil, fmt.Errorf("业务返回错误: code=%d msg=%s", res.Code, res.Msg)
	}

	return &res.Data, nil
}

// GetPlans 获取公开市场在售套餐列表（带鉴权）
func (c *Client) GetPlans(ctx context.Context) ([]PublicPlan, error) {
	url := fmt.Sprintf("%s/plans", narwhalBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	c.injectHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求接口失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := 30 * time.Second
		if h := resp.Header.Get("Retry-After"); h != "" {
			if s, err := strconv.Atoi(h); err == nil && s > 0 {
				retryAfter = time.Duration(s) * time.Second
			}
		}
		return nil, &RateLimitError{
			StatusCode: resp.StatusCode,
			RetryAfter: retryAfter,
			Message:    "已触碰平台 /plans 10次/分 调用上限",
		}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("接口返回非 200 状态码: %d", resp.StatusCode)
	}

	var res ApiResponse[PlansData]
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("解析套餐响应失败: %w", err)
	}

	if res.Code != 0 {
		return nil, fmt.Errorf("业务返回错误: code=%d msg=%s", res.Code, res.Msg)
	}

	c.mu.Lock()
	c.cachedPlans = res.Data.Plans
	c.cacheTime = time.Now()
	c.mu.Unlock()

	return res.Data.Plans, nil
}

// GetCachedPlans 获取内存中最近一次成功轮询的套餐快照（0ms 即时响应，无请求开销与限速风险）
func (c *Client) GetCachedPlans() ([]PublicPlan, time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.cachedPlans) == 0 {
		return nil, time.Time{}
	}
	plans := make([]PublicPlan, len(c.cachedPlans))
	copy(plans, c.cachedPlans)
	return plans, c.cacheTime
}

// injectHeaders 统一注入请求头（User-Agent, Accept, 以及关键的 Authorization Bearer 鉴权）
func (c *Client) injectHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "NarwhalMonitorBot/1.0")
	req.Header.Set("Accept", "application/json")
	if auth := c.getAuthHeader(); auth != "" {
		req.Header.Set("Authorization", auth)
	}
}
