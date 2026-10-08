package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"narwhal-monitor/internal/api"
	"narwhal-monitor/internal/bot"
	"narwhal-monitor/internal/config"
	"narwhal-monitor/internal/filter"
	"narwhal-monitor/internal/monitor"
	"narwhal-monitor/internal/storage"
)

func main() {
	envPath := flag.String("env", ".env", "环境变量配置文件路径 (.env)")
	flag.Parse()

	log.Println("==================================================")
	log.Println("🐳 Narwhal Cloud Telegram Monitor Bot 启动中...")
	log.Println("==================================================")

	// 1. 加载 .env 配置
	cfg, err := config.Load(*envPath)
	if err != nil {
		log.Fatalf("[致命错误] 加载配置失败: %v", err)
	}

	// 2. 初始化持久化存储与日志双写落盘
	if err := os.MkdirAll(cfg.Storage.DataDir, 0755); err != nil {
		log.Fatalf("[致命错误] 创建数据目录失败: %v", err)
	}

	logFilePath := filepath.Join(cfg.Storage.DataDir, "app.log")
	if customLog := strings.TrimSpace(os.Getenv("LOG_FILE")); customLog != "" {
		logFilePath = customLog
	}

	// 单文件超过 20MB 时自动轮转备份
	if fi, err := os.Stat(logFilePath); err == nil && fi.Size() > 20*1024*1024 {
		_ = os.Rename(logFilePath, logFilePath+".old")
	}

	logFile, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("[日志提示] 无法打开日志文件 %s: %v，保持控制台输出", logFilePath, err)
	} else {
		defer logFile.Close()
		log.SetOutput(io.MultiWriter(os.Stdout, logFile))
		log.Printf("[日志系统] 已启用双写输出，落盘日志路径: %s", logFilePath)
	}

	store, err := storage.NewStore(cfg.Storage.DataDir)
	if err != nil {
		log.Fatalf("[致命错误] 初始化存储模块失败: %v", err)
	}

	// 3. 初始化过滤规则引擎，绑定异步保存
	engine := filter.NewEngine(func(chats map[int64]*filter.ChatConfig) {
		if err := store.SaveRules(chats); err != nil {
			log.Printf("[存储错误] 异步保存过滤规则失败: %v", err)
		}
	})

	// 绑定全局系统配置持久化
	engine.SetOnSaveSettings(func(enabled bool, maxGuests int) {
		if err := store.SaveSettings(&storage.SystemSettings{
			GuestModeEnabled: enabled,
			MaxGuests:        maxGuests,
		}); err != nil {
			log.Printf("[存储错误] 异步保存系统配置失败: %v", err)
		}
	})

	// 加载系统配置 (游客模式与名额限制)
	if sysSettings, err := store.LoadSettings(); err == nil && sysSettings != nil {
		engine.InitSettings(sysSettings.GuestModeEnabled, sysSettings.MaxGuests)
		guestStatus := "🔴 关闭"
		if sysSettings.GuestModeEnabled {
			guestStatus = "🟢 开启"
		}
		log.Printf("[系统配置] 游客模式: %s | 名额上限: %d 人", guestStatus, sysSettings.MaxGuests)
	}

	// 绑定超级管理员（自动赋予永久白名单授权与推送）
	engine.SetAdminID(cfg.Telegram.AdminID)
	log.Printf("[鉴权] 超级管理员 ID 已绑定: %d", cfg.Telegram.AdminID)

	// 加载历史规则
	savedConfigs, err := store.LoadRules()
	if err != nil {
		log.Printf("[存储警告] 加载历史规则失败，采用空白初始化: %v", err)
	} else {
		if err := engine.LoadConfigs(savedConfigs); err != nil {
			log.Printf("[规则警告] 预编译历史规则异常: %v", err)
		} else {
			log.Printf("[规则] 成功载入 %d 个 Chat 的历史规则与设置", len(savedConfigs))
		}
	}

	// 4. 初始化 API 客户端并处理鉴权认证
	apiClient := api.NewClient(cfg.Monitor.APIKey)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 若未填 API_KEY 但提供了邮箱与密码，尝试自动登录获取 Token
	if cfg.Monitor.APIKey == "" && cfg.Monitor.Email != "" && cfg.Monitor.Password != "" {
		log.Println("[鉴权] 正在使用邮箱与密码登录 Narwhal 平台...")
		loginRes, err := apiClient.Login(ctx, cfg.Monitor.Email, cfg.Monitor.Password)
		if err != nil {
			log.Printf("[鉴权失败] 账号登录失败: %v", err)
		} else {
			log.Printf("[鉴权成功] 登录成功！用户: %s (角色: %s)", loginRes.Email, loginRes.Role)
		}
	}

	// 校验鉴权状态
	if apiClient.HasAuth() {
		me, err := apiClient.GetMe(ctx)
		if err != nil {
			log.Printf("[鉴权警告] 校验 API 凭据异常: %v", err)
		} else {
			log.Printf("[鉴权就绪] 成功认证 Narwhal 账户: %s | 身份: %s | 余额: $%.2f", me.Email, me.Role, me.AvailableBalance)
		}
	} else {
		log.Println("[鉴权提示] 未配置 API_KEY，当前以公开访客身份访问（建议在 .env 中设置 API_KEY）")
	}

	// 5. 初始化 Telegram Bot 实例与卡片生命周期追踪器
	stat, err := store.LoadState()
	if err != nil {
		log.Printf("[存储提示] 初始化监控状态快照: %v", err)
	}
	cardTracker := bot.NewCardTracker(stat.TrackedCards)

	tgBot, err := bot.NewBot(bot.Config{
		Token:   cfg.Telegram.BotToken,
		AdminID: cfg.Telegram.AdminID,
	}, engine, apiClient)
	if err != nil {
		log.Fatalf("[致命错误] 初始化 Telegram Bot 失败: %v", err)
	}
	tgBot.SetTracker(cardTracker)

	// 6. 初始化监控差分轮询器
	poller, err := monitor.NewPoller(
		apiClient,
		store,
		time.Duration(cfg.Monitor.PollIntervalSeconds)*time.Second,
		func(evt monitor.Event) {
			tgBot.DispatchEvent(evt)
		},
	)
	if err != nil {
		log.Fatalf("[致命错误] 初始化监控轮询器失败: %v", err)
	}
	poller.SetCardProvider(cardTracker)

	// 7. 发送上线就绪通知给超级管理员
	if cfg.Telegram.StartupNotify && cfg.Telegram.AdminID != 0 {
		go func() {
			time.Sleep(1 * time.Second) // 等待 bot 完全建立连接
			tgBot.SendStartupNotify(cfg.Telegram.AdminID, cfg.Monitor.PollIntervalSeconds)
		}()
	}

	// 8. 启动监控轮询器
	go poller.Start(ctx)

	// 9. 监听系统退出信号，优雅停机
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Printf("[系统] 收到退出信号 (%v)，正在执行优雅停机...", sig)
		cancel()
		tgBot.Stop()
		engine.Close()
		time.Sleep(500 * time.Millisecond)
		log.Println("[系统] 服务已安全退出。")
		os.Exit(0)
	}()

	// 10. 启动 Telegram Bot 消息监听（阻塞主线程）
	tgBot.Start()
}
