package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config 定义整体服务配置（全面统一采用 .env / 环境变量）
type Config struct {
	Telegram TelegramConfig
	Monitor  MonitorConfig
	Storage  StorageConfig
}

type TelegramConfig struct {
	BotToken      string // Telegram Bot Token
	AdminID       int64  // 单一超级管理员 ID
	StartupNotify bool   // 启动上线时是否发送问候通知
}

type MonitorConfig struct {
	APIKey              string // Narwhal 平台鉴权 API Key (rnm_...)
	Email               string // Narwhal 平台账号邮箱 (可选)
	Password            string // Narwhal 平台密码 (可选)
	PollIntervalSeconds int    // 轮询周期秒数
}

type StorageConfig struct {
	DataDir string // 本地持久化数据目录
}

// Load 从指定 .env 文件（默认 .env）或系统环境变量中加载配置
func Load(envFilePath string) (*Config, error) {
	if envFilePath == "" {
		envFilePath = ".env"
	}

	// 尝试加载指定的 .env 文件
	loadDotEnv(envFilePath)

	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" || token == "123456789:ABCdefGhIJKlmNoPQRstuVWXyz" {
		return nil, fmt.Errorf("未在 %s 中检测到有效的 BOT_TOKEN，请参考 .env.example 进行配置", envFilePath)
	}

	cfg := &Config{
		Telegram: TelegramConfig{
			BotToken:      token,
			StartupNotify: true,
		},
		Monitor: MonitorConfig{
			PollIntervalSeconds: 15,
		},
		Storage: StorageConfig{
			DataDir: "./data",
		},
	}

	// Narwhal 平台鉴权凭据 (API Key 优先，或邮箱密码)
	if key := strings.TrimSpace(os.Getenv("API_KEY")); key != "" {
		cfg.Monitor.APIKey = key
	} else if key := strings.TrimSpace(os.Getenv("NARWHAL_API_KEY")); key != "" {
		cfg.Monitor.APIKey = key
	}
	cfg.Monitor.Email = strings.TrimSpace(os.Getenv("NARWHAL_EMAIL"))
	cfg.Monitor.Password = strings.TrimSpace(os.Getenv("NARWHAL_PASSWORD"))

	// 轮询周期
	if intervalStr := strings.TrimSpace(os.Getenv("POLL_INTERVAL")); intervalStr != "" {
		if sec, err := strconv.Atoi(intervalStr); err == nil && sec > 0 {
			cfg.Monitor.PollIntervalSeconds = sec
		}
	}
	if cfg.Monitor.PollIntervalSeconds < 12 {
		cfg.Monitor.PollIntervalSeconds = 15 // 限速安全保底
	}

	// 本地数据目录
	if dataDir := strings.TrimSpace(os.Getenv("DATA_DIR")); dataDir != "" {
		cfg.Storage.DataDir = dataDir
	}

	// 超级管理员 ID 解析 (严格要求单一 ADMIN_ID)
	adminIDStr := strings.TrimSpace(os.Getenv("ADMIN_ID"))
	if adminIDStr == "" {
		return nil, fmt.Errorf("未在 %s 中检测到 ADMIN_ID，请配置超级管理员 Telegram ID", envFilePath)
	}
	adminID, err := strconv.ParseInt(adminIDStr, 10, 64)
	if err != nil || adminID == 0 {
		return nil, fmt.Errorf("ADMIN_ID 格式不合法（必须为非零数字）: %s", adminIDStr)
	}
	cfg.Telegram.AdminID = adminID

	// 启动通知开关
	if notifyStr := strings.TrimSpace(os.Getenv("STARTUP_NOTIFY")); notifyStr != "" {
		cfg.Telegram.StartupNotify = strings.ToLower(notifyStr) != "false" && notifyStr != "0"
	}

	return cfg, nil
}

// loadDotEnv 原生高效解析 .env 文件，零外部依赖
func loadDotEnv(filename string) {
	f, err := os.Open(filename)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			k := strings.TrimSpace(parts[0])
			v := strings.TrimSpace(parts[1])
			v = strings.Trim(v, `"'`)
			if os.Getenv(k) == "" {
				_ = os.Setenv(k, v)
			}
		}
	}
}
