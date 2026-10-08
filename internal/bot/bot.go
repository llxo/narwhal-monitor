package bot

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"narwhal-monitor/internal/api"
	"narwhal-monitor/internal/filter"
	"narwhal-monitor/internal/monitor"
)

type pushTask struct {
	chatID     int64
	text       string
	markup     *tele.ReplyMarkup
	msgID      int
	machineKey string
	stocks     map[string]int
	isEdit     bool
	isEditOnly bool
	isSoldOut  bool
}

// Bot 是 Telegram 机器人的核心包装器
type Bot struct {
	teleBot    *tele.Bot
	engine     *filter.Engine
	handler    *Handler
	tracker    *CardTracker
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

	// 1. 注册全员默认命令菜单 (普通白名单用户与游客)
	userCommands := []tele.Command{
		{Text: "start", Description: "开启监控向导与欢迎信息"},
		{Text: "register", Description: "登记为游客"},
		{Text: "guest", Description: "游客模式管理"},
		{Text: "menu", Description: "打开交互式过滤菜单面板"},
		{Text: "filter", Description: "查看或配置高级正则过滤规则"},
		{Text: "sub", Description: "切换推送通知 (开启/暂停)"},
		{Text: "mute", Description: "开启临时免打扰 (/mute 1h)"},
		{Text: "id", Description: "查看当前会话的 Chat ID"},
		{Text: "help", Description: "查看命令语法与使用教程"},
	}

	if err := b.SetCommands(userCommands, tele.CommandScope{Type: tele.CommandScopeDefault}); err != nil {
		log.Printf("[Bot警告] 注册全员默认命令菜单失败: %v", err)
	} else {
		log.Println("[Bot] 官方全员默认命令菜单已成功向 Telegram 注册！")
	}

	// 2. 为单一超级管理员注册专属特权菜单 (包含 /admin, /user, /guest, /check, /status, /stats, /broadcast)
	if cfg.AdminID != 0 {
		adminCommands := []tele.Command{
			{Text: "start", Description: "开启监控向导与欢迎信息"},
			{Text: "menu", Description: "打开交互式过滤菜单面板"},
			{Text: "filter", Description: "查看或配置高级正则过滤规则"},
			{Text: "sub", Description: "切换推送通知 (开启/暂停)"},
			{Text: "mute", Description: "开启临时免打扰 (/mute 1h)"},
			{Text: "id", Description: "查看当前会话的 Chat ID"},
			{Text: "help", Description: "查看命令语法与使用教程"},
			{Text: "admin", Description: "👑 [管理] 打开管理员控制面板"},
			{Text: "check", Description: "👑 [管理] 查询当前在售库存与命中"},
			{Text: "status", Description: "👑 [管理] 查看系统运行状态"},
			{Text: "stats", Description: "👑 [管理] 查看系统资源与账号统计"},
			{Text: "user", Description: "👑 [管理] 白名单授权管理"},
			{Text: "guest", Description: "👑 [管理] 游客模式设置"},
			{Text: "broadcast", Description: "👑 [管理] 向全员群发系统维护广播"},
		}
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

// SetTracker 设置卡片消息生命周期追踪器
func (b *Bot) SetTracker(tracker *CardTracker) {
	b.tracker = tracker
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

// parseFloodWait 解析 Telegram 返回的 429 Flood Wait 冷却秒数
func parseFloodWait(err error) time.Duration {
	if err == nil {
		return 0
	}
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "too many requests") || strings.Contains(errStr, "flood") {
		re := regexp.MustCompile(`(?:retry after|flood wait of) (\d+)`)
		matches := re.FindStringSubmatch(errStr)
		if len(matches) > 1 {
			if secs, err := strconv.Atoi(matches[1]); err == nil && secs > 0 {
				return time.Duration(secs) * time.Second
			}
		}
		// 默认兜底冷却 5 秒
		return 5 * time.Second
	}
	return 0
}

func (b *Bot) runPushWorker() {
	// 全局发信控制：每 40ms 最多发出一条消息 (全局上限 25 msg/s，符合 Telegram 30 msg/s 规范)
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()

	var floodUntil time.Time

	for {
		select {
		case <-b.stopChan:
			return
		case task, ok := <-b.pushQueue:
			if !ok {
				return
			}

			// 检查是否处于 Telegram 429 Flood Wait 熔断冷却期
			if time.Now().Before(floodUntil) {
				waitDur := time.Until(floodUntil)
				log.Printf("[推送退避] 处于 Telegram 429 熔断冷却中，等待 %v...", waitDur)
				select {
				case <-time.After(waitDur):
				case <-b.stopChan:
					return
				}
			}

			<-ticker.C

			// 单 Chat 限速保护：对同一 Chat ID 两次推送至少间隔 1000ms，严格遵守 Telegram 1 msg/s 规范
			b.lastSendMu.Lock()
			lastTime := b.lastSend[task.chatID]
			waitDur := time.Second - time.Since(lastTime)
			if waitDur > 0 {
				b.lastSendMu.Unlock()
				// 非阻塞延后重入队：该 Chat 处于 1s 冷却期，延后重新入队，先放行队列中其他 Chat
				go func(t pushTask, delay time.Duration) {
					select {
					case <-time.After(delay):
						select {
						case b.pushQueue <- t:
						default:
						}
					case <-b.stopChan:
					}
				}(task, waitDur)
				continue
			}
			b.lastSend[task.chatID] = time.Now()
			b.lastSendMu.Unlock()

			// 发送或原地编辑消息
			chat := &tele.Chat{ID: task.chatID}
			var sentMsg *tele.Message
			var err error

			if task.isEdit && task.msgID > 0 {
				msgToEdit := &tele.Message{ID: task.msgID, Chat: chat}
				if task.markup != nil && len(task.markup.InlineKeyboard) > 0 {
					sentMsg, err = b.teleBot.Edit(msgToEdit, task.text, task.markup, tele.ModeHTML, tele.NoPreview)
				} else {
					sentMsg, err = b.teleBot.Edit(msgToEdit, task.text, tele.ModeHTML, tele.NoPreview)
				}

				if err == nil {
					if b.tracker != nil && task.machineKey != "" {
						b.tracker.RecordEdit(task.machineKey, task.chatID, task.stocks, task.isSoldOut)
					}
					log.Printf("[编辑成功] 已原地更新 Chat %d 的卡片 | 机器: %s | MsgID: %d", task.chatID, task.machineKey, task.msgID)
				} else {
					errLower := strings.ToLower(err.Error())
					if strings.Contains(errLower, "message is not modified") {
						err = nil // 内容无变动，非错误
					} else if strings.Contains(errLower, "message to edit not found") || strings.Contains(errLower, "message can't be edited") {
						// 旧消息已失效或被删除
						if b.tracker != nil && task.machineKey != "" {
							b.tracker.InvalidateMessage(task.machineKey, task.chatID)
						}
						// 仅在非纯扣减更新时降级重新发送新卡片
						if !task.isEditOnly {
							if task.markup != nil && len(task.markup.InlineKeyboard) > 0 {
								sentMsg, err = b.teleBot.Send(chat, task.text, task.markup, tele.ModeHTML, tele.NoPreview)
							} else {
								sentMsg, err = b.teleBot.Send(chat, task.text, tele.ModeHTML, tele.NoPreview)
							}
							if err == nil && sentMsg != nil && b.tracker != nil && task.machineKey != "" {
								b.tracker.RecordPush(task.machineKey, task.chatID, sentMsg.ID, task.stocks, task.isSoldOut)
								log.Printf("[推送成功(降级)] 已向 Chat %d 发送新卡片 | 机器: %s | MsgID: %d", task.chatID, task.machineKey, sentMsg.ID)
							}
						} else {
							err = nil // 纯售罄/扣减更新且旧消息已不在，静默跳过
						}
					}
				}
			} else {
				if task.markup != nil && len(task.markup.InlineKeyboard) > 0 {
					sentMsg, err = b.teleBot.Send(chat, task.text, task.markup, tele.ModeHTML, tele.NoPreview)
				} else {
					sentMsg, err = b.teleBot.Send(chat, task.text, tele.ModeHTML, tele.NoPreview)
				}
				if err == nil && sentMsg != nil && b.tracker != nil && task.machineKey != "" {
					b.tracker.RecordPush(task.machineKey, task.chatID, sentMsg.ID, task.stocks, task.isSoldOut)
					log.Printf("[推送成功] 已向 Chat %d 发送新卡片 | 机器: %s | MsgID: %d", task.chatID, task.machineKey, sentMsg.ID)
				}
			}
			if err != nil {
				log.Printf("[推送错误] 向 Chat %d 推送消息失败: %v", task.chatID, err)

				// 检查 429 Flood Wait 错误并进入退避
				if floodWait := parseFloodWait(err); floodWait > 0 {
					floodUntil = time.Now().Add(floodWait)
					log.Printf("[推送熔断] 触发 Telegram 429 Flood Wait，自动全局暂停推送 %v (至 %s)", floodWait, floodUntil.Format("15:04:05"))
					// 将本条失败的任务重新塞回队列头部稍后重试
					go func(t pushTask, delay time.Duration) {
						select {
						case <-time.After(delay):
							select {
							case b.pushQueue <- t:
							default:
							}
						case <-b.stopChan:
						}
					}(task, floodWait)
					continue
				}

				// 死信自愈处理
				errStr := strings.ToLower(err.Error())
				if strings.Contains(errStr, "blocked") || strings.Contains(errStr, "deactivated") || strings.Contains(errStr, "chat not found") {
					if b.engine.IsGuest(task.chatID) {
						b.engine.UnregisterGuest(task.chatID)
						log.Printf("[推送自愈] 检测到游客 Chat %d 已拉黑或注销，已自动注销并释放名额", task.chatID)
					} else {
						b.engine.SetSubscribed(task.chatID, false)
						log.Printf("[推送自愈] 检测到白名单 Chat %d 已拉黑或注销，已自动暂停其推送订阅", task.chatID)
					}
				}
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
	case monitor.EventPlanNew, monitor.EventPlanRestock, monitor.EventPlanUpdate:
		cardText, markup = RenderPlanCard(evt)
	default:
		return
	}

	if cardText == "" {
		return
	}

	// 汇总该母机所有套餐的最新库存快照（涵盖触发套餐与同机其他套餐）并判定母机是否全盘售罄
	allStocks := make(map[string]int)
	allSoldOut := true
	for _, p := range evt.TriggeredPlans {
		rem := p.Remaining
		if p.SoldOut || p.RamInsufficient {
			rem = 0
		} else if rem == 0 && !p.SoldOut {
			rem = -1
		}
		allStocks[p.ID] = rem
		if rem != 0 {
			allSoldOut = false
		}
	}
	for _, p := range evt.OtherPlans {
		rem := p.Remaining
		if p.SoldOut || p.RamInsufficient {
			rem = 0
		} else if rem == 0 && !p.SoldOut {
			rem = -1
		}
		allStocks[p.ID] = rem
		if rem != 0 {
			allSoldOut = false
		}
	}

	// 区分优先级分流：超级管理员与正式白名单享有最高抢占优先级，游客排在其后
	var vipChats []int64
	var guestChats []int64
	adminID := b.engine.GetAdminID()

	for _, chatID := range subscribers {
		if !b.engine.Evaluate(chatID, &evt.Payload) {
			continue
		}
		if (adminID != 0 && chatID == adminID) || b.engine.IsAuthorized(chatID) {
			vipChats = append(vipChats, chatID)
		} else {
			guestChats = append(guestChats, chatID)
		}
	}

	// 优先调度 VIP 用户与管理员，再调度游客
	dispatchList := append(vipChats, guestChats...)

	for _, chatID := range dispatchList {
		var targetMsgID int
		var shouldEdit bool

		if b.tracker != nil && evt.MachineKey != "" {
			isRestock := (evt.Type == monitor.EventPlanRestock)
			isNew := (evt.Type == monitor.EventPlanNew)
			targetMsgID, shouldEdit = b.tracker.ShouldEdit(evt.MachineKey, chatID, isRestock, isNew, evt.IsEditOnly, allStocks)
		}

		// 纯扣减/售罄编辑事件：若该 Chat 之前从未发送过卡片，则无需发新消息打扰
		if evt.IsEditOnly && (!shouldEdit || targetMsgID <= 0) {
			continue
		}

		task := pushTask{
			chatID:     chatID,
			text:       cardText,
			markup:     markup,
			msgID:      targetMsgID,
			machineKey: evt.MachineKey,
			stocks:     allStocks,
			isEdit:     shouldEdit && targetMsgID > 0,
			isEditOnly: evt.IsEditOnly,
			isSoldOut:  allSoldOut,
		}

		select {
		case b.pushQueue <- task:
		default:
			log.Printf("[推送警告] 推送队列已满，丢弃发往 Chat %d 的消息", chatID)
		}
	}
}
