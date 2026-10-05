package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
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

	// 2. 初始化持久化存储
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

	// 5. 初始化 Telegram Bot 实例
	tgBot, err := bot.NewBot(bot.Config{
		Token:   cfg.Telegram.BotToken,
		AdminID: cfg.Telegram.AdminID,
	}, engine, apiClient)
	if err != nil {
		log.Fatalf("[致命错误] 初始化 Telegram Bot 失败: %v", err)
	}

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
