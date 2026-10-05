package bot

import (
	"fmt"
	"log"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"narwhal-monitor/internal/api"
	"narwhal-monitor/internal/filter"
	"narwhal-monitor/internal/monitor"
)

type pushTask struct {
	chatID int64
	text   string
	markup *tele.ReplyMarkup
}

// Bot 是 Telegram 机器人的核心包装器
type Bot struct {
	teleBot    *tele.Bot
	engine     *filter.Engine
	handler    *Handler
	pushQueue  chan pushTask
	stopChan   chan struct{}
	lastSendMu sync.Mutex
	lastSend   map[int64]time.Time
}

// Config 是 Bot 的配置结构
type Config struct {
	Token   string
	AdminID int64
}

// NewBot 初始化并创建 Telegram 机器人
func NewBot(cfg Config, engine *filter.Engine, apiClient *api.Client) (*Bot, error) {
	if cfg.Token == "" {
		return nil, fmt.Errorf("bot_token 不能为空")
	}

	pref := tele.Settings{
		Token:  cfg.Token,
		Poller: &tele.LongPoller{Timeout: 10 * time.Second},
		OnError: func(err error, c tele.Context) {
			log.Printf("[Bot错误] %v", err)
		},
	}

	b, err := tele.NewBot(pref)
	if err != nil {
		return nil, fmt.Errorf("创建 Telebot 实例失败: %w", err)
	}

	handler := NewHandler(engine, apiClient, cfg.AdminID)
	handler.RegisterRoutes(b)

	// 1. 注册全员默认命令菜单 (普通白名单用户)
	userCommands := []tele.Command{
		{Text: "check", Description: "立即查询当前在售库存与命中情况"},
		{Text: "menu", Description: "打开内联交互式过滤菜单面板"},
		{Text: "filter", Description: "查看或配置高级正则过滤规则"},
		{Text: "sub", Description: "开关通知推送 (/sub on|off)"},
		{Text: "mute", Description: "开启临时免打扰 (/mute 1h)"},
		{Text: "id", Description: "查看当前会话的 Chat ID"},
		{Text: "status", Description: "查看监控服务运行状态"},
		{Text: "help", Description: "查看命令语法与使用教程"},
	}

	if err := b.SetCommands(userCommands, tele.CommandScope{Type: tele.CommandScopeDefault}); err != nil {
		log.Printf("[Bot警告] 注册全员默认命令菜单失败: %v", err)
	} else {
		log.Println("[Bot] 官方全员默认命令菜单已成功向 Telegram 注册！")
	}

	// 2. 为单一超级管理员注册专属特权菜单 (包含 /admin, /user, /stats, /broadcast)
	if cfg.AdminID != 0 {
		adminCommands := append([]tele.Command(nil), userCommands...)
		adminCommands = append(adminCommands,
			tele.Command{Text: "admin", Description: "👑 [管理] 打开管理员控制面板"},
			tele.Command{Text: "user", Description: "👑 [管理] 白名单授权管理 (/user add|del|list)"},
			tele.Command{Text: "stats", Description: "👑 [管理] 查看系统监控大盘统计"},
			tele.Command{Text: "broadcast", Description: "👑 [管理] 向全员群发系统维护广播"},
		)
		scope := tele.CommandScope{Type: tele.CommandScopeChat, ChatID: cfg.AdminID}
		if err := b.SetCommands(adminCommands, scope); err != nil {
			log.Printf("[Bot提示] 管理员 %d 专属特权菜单初始化登记: %v", cfg.AdminID, err)
		} else {
			log.Printf("[Bot] 已成功向 Telegram 为超级管理员 %d 注册专属特权命令菜单！", cfg.AdminID)
		}
	}

	bInstance := &Bot{
		teleBot:   b,
		engine:    engine,
		handler:   handler,
		pushQueue: make(chan pushTask, 2048),
		stopChan:  make(chan struct{}),
		lastSend:  make(map[int64]time.Time),
	}
	go bInstance.runPushWorker()
	return bInstance, nil
}

// Start 启动机器人监听（阻塞）
func (b *Bot) Start() {
	log.Printf("[Bot] Telegram Bot @%s 启动成功，开始监听消息与指令...", b.teleBot.Me.Username)
	b.teleBot.Start()
}

// Stop 停止机器人监听
func (b *Bot) Stop() {
	select {
	case <-b.stopChan:
	default:
		close(b.stopChan)
	}
	b.teleBot.Stop()
}

func (b *Bot) runPushWorker() {
	// 全局发信控制：每 40ms 最多发出一条消息 (全局上限 25 msg/s，符合 Telegram 30 msg/s 规范)
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-b.stopChan:
			return
		case task, ok := <-b.pushQueue:
			if !ok {
				return
			}
			<-ticker.C

			// 单 Chat 限速保护：对同一 Chat ID 两次推送至少间隔 1000ms，严格遵守 Telegram 1 msg/s 规范
			b.lastSendMu.Lock()
			lastTime := b.lastSend[task.chatID]
			waitDur := time.Second - time.Since(lastTime)
			if waitDur > 0 {
				b.lastSendMu.Unlock()
				select {
				case <-time.After(waitDur):
				case <-b.stopChan:
					return
				}
				b.lastSendMu.Lock()
			}
			b.lastSend[task.chatID] = time.Now()
			b.lastSendMu.Unlock()

			// 发送消息
			chat := &tele.Chat{ID: task.chatID}
			var err error
			if task.markup != nil && len(task.markup.InlineKeyboard) > 0 {
				_, err = b.teleBot.Send(chat, task.text, task.markup, tele.ModeHTML, tele.NoPreview)
			} else {
				_, err = b.teleBot.Send(chat, task.text, tele.ModeHTML, tele.NoPreview)
			}
			if err != nil {
				log.Printf("[推送错误] 向 Chat %d 推送消息失败: %v", task.chatID, err)
			}
		}
	}
}

// SendStartupNotify 发送启动上线就绪通知给超级管理员
func (b *Bot) SendStartupNotify(adminID int64, interval int) {
	if adminID == 0 {
		return
	}
	msg := fmt.Sprintf("🟢 <b>【Narwhal Monitor Bot 已启动上线】</b>\n\n"+
		"• <b>运行状态:</b> 正常监听中\n"+
		"• <b>轮询周期:</b> 每 %d 秒扫描一次\n"+
		"• <b>超级管理员:</b> <code>%d</code> (您自身)\n"+
		"• <b>快捷指令:</b>\n"+
		"  - 发送 <code>/check</code> 立即获取当前各地区实时库存\n"+
		"  - 发送 <code>/menu</code> 点击按钮快速定制地区与限价\n"+
		"  - 发送 <code>/admin</code> 打开管理员控制中心\n\n"+
		"<i>💡 监控已就绪，有符合偏好的补货将在此第一时间推送！</i>", interval, adminID)

	_, err := b.teleBot.Send(&tele.Chat{ID: adminID}, msg, tele.ModeHTML)
	if err != nil {
		log.Printf("[通知提示] 发送上线通知到管理员 %d 失败: %v", adminID, err)
	}
}

// DispatchEvent 根据过滤引擎规则分发推送卡片到所有满足条件的 Chat
func (b *Bot) DispatchEvent(evt monitor.Event) {
	subscribers := b.engine.GetAllSubscribedChats()
	if len(subscribers) == 0 {
		return
	}

	var cardText string
	var markup *tele.ReplyMarkup

	switch evt.Type {
	case monitor.EventPlanNew, monitor.EventPlanRestock:
		cardText, markup = RenderPlanCard(evt)
	default:
		return
	}

	if cardText == "" {
		return
	}

	for _, chatID := range subscribers {
		// 实时通过过滤引擎判定当前 Chat 是否命中规则
		if !b.engine.Evaluate(chatID, &evt.Payload) {
			continue
		}

		select {
		case b.pushQueue <- pushTask{chatID: chatID, text: cardText, markup: markup}:
		default:
			log.Printf("[推送警告] 推送队列已满，丢弃发往 Chat %d 的消息", chatID)
		}
	}
}
