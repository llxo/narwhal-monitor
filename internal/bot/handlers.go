package bot

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"narwhal-monitor/internal/api"
	"narwhal-monitor/internal/filter"
)

// Handler 集中管理 Telegram 命令与内联按钮回调逻辑
type Handler struct {
	engine         *filter.Engine
	apiClient      *api.Client
	adminID        int64
	startTime      time.Time
	checkMu        sync.Mutex
	lastDirectPoll time.Time
}

// NewHandler 创建 Handler 实例
func NewHandler(engine *filter.Engine, apiClient *api.Client, adminID int64) *Handler {
	return &Handler{
		engine:    engine,
		apiClient: apiClient,
		adminID:   adminID,
		startTime: time.Now(),
	}
}

// RegisterRoutes 注册所有命令与交互回调
func (h *Handler) RegisterRoutes(b *tele.Bot) {
	// 全局白名单拦截中间件
	b.Use(h.authMiddleware)

	// 基础命令
	b.Handle("/start", h.HandleStart)
	b.Handle("/check", h.HandleCheck)
	b.Handle("/help", h.HandleHelp)
	b.Handle("/menu", h.HandleMenu)
	b.Handle("/filter", h.HandleFilter)
	b.Handle("/sub", h.HandleSub)
	b.Handle("/mute", h.HandleMute)
	b.Handle("/status", h.HandleStatus)
	b.Handle("/id", h.HandleID)
	b.Handle("/chatid", h.HandleID)

	// 管理员专属命令
	b.Handle("/admin", h.HandleAdmin)
	b.Handle("/user", h.HandleUser)
	b.Handle("/stats", h.HandleStats)
	b.Handle("/broadcast", h.HandleBroadcast)

	// 管理员专属内联按钮
	b.Handle(&tele.Btn{Unique: "btn_admin_menu"}, h.HandleAdmin)
	b.Handle(&tele.Btn{Unique: "btn_admin_user_list"}, func(c tele.Context) error {
		if !h.isAdmin(c) {
			return c.Respond(&tele.CallbackResponse{Text: "⚠️ 仅管理员有权操作", ShowAlert: true})
		}
		if c.Chat().Type != tele.ChatPrivate {
			return c.Respond(&tele.CallbackResponse{Text: "⚠️ 白名单列表包含用户隐私，仅限在私聊中查看", ShowAlert: true})
		}
		_ = c.Respond()
		auths := h.engine.GetAllAuthorizedChats()
		if len(auths) == 0 {
			return c.Send("📋 当前白名单列表为空。", tele.ModeHTML)
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("📋 <b>【当前授权白名单列表 (共 %d 个)】</b>\n\n", len(auths)))
		for i, cfg := range auths {
			roleIcon := "👤"
			if cfg.ChatID == h.adminID {
				roleIcon = "👑"
			} else if cfg.ChatID < 0 {
				roleIcon = "👥"
			}
			statusStr := "🔔 接收中"
			if !cfg.Subscribed {
				statusStr = "🔕 已暂停"
			} else if !cfg.MutedUntil.IsZero() && time.Now().Before(cfg.MutedUntil) {
				statusStr = "🟡 静音中"
			}
			remarkStr := ""
			if cfg.Remark != "" {
				remarkStr = fmt.Sprintf(" (<b>%s</b>)", html.EscapeString(cfg.Remark))
			}
			sb.WriteString(fmt.Sprintf("<b>#%d</b> %s <code>%d</code>%s\n", i+1, roleIcon, cfg.ChatID, remarkStr))
			sb.WriteString(fmt.Sprintf("   ├ 状态: %s | 规则: %d 条\n", statusStr, len(cfg.Rules)))
			regStr := "全地区"
			if len(cfg.QuickRegions) > 0 {
				regStr = strings.Join(cfg.QuickRegions, ",")
			}
			priceStr := "不限价"
			if cfg.QuickMaxPrice > 0 {
				priceStr = fmt.Sprintf("≤$%.2f", cfg.QuickMaxPrice)
			}
			sb.WriteString(fmt.Sprintf("   └ 偏好: %s | %s\n\n", regStr, priceStr))
		}
		return c.Send(sb.String(), tele.ModeHTML)
	})
	b.Handle(&tele.Btn{Unique: "btn_admin_stats"}, func(c tele.Context) error {
		if !h.isAdmin(c) {
			return c.Respond(&tele.CallbackResponse{Text: "⚠️ 仅管理员有权操作", ShowAlert: true})
		}
		if c.Chat().Type != tele.ChatPrivate {
			return c.Respond(&tele.CallbackResponse{Text: "⚠️ 系统大盘包含财务敏感信息，仅限在私聊中查看", ShowAlert: true})
		}
		_ = c.Respond()
		return h.HandleStats(c)
	})

	// 内联按钮回调
	b.Handle(&tele.Btn{Unique: "btn_reg"}, h.HandleBtnRegion)
	b.Handle(&tele.Btn{Unique: "btn_price"}, h.HandleBtnPrice)
	b.Handle(&tele.Btn{Unique: "btn_sub_toggle"}, h.HandleBtnSubToggle)
	b.Handle(&tele.Btn{Unique: "btn_refresh"}, h.HandleBtnRefresh)

	// 更多冷门地区二级内联按钮
	b.Handle(&tele.Btn{Unique: "btn_rare_menu"}, h.HandleBtnRareMenu)
	b.Handle(&tele.Btn{Unique: "btn_rare_toggle"}, h.HandleBtnRareToggle)
	b.Handle(&tele.Btn{Unique: "btn_rare_all"}, h.HandleBtnRareAll)
	b.Handle(&tele.Btn{Unique: "btn_rare_clear"}, h.HandleBtnRareClear)
	b.Handle(&tele.Btn{Unique: "btn_menu_home"}, h.HandleBtnMenuHome)
}

// authMiddleware 拦截未授权用户，仅允许白名单用户与管理员使用
func (h *Handler) authMiddleware(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		senderID := c.Sender().ID
		chatID := c.Chat().ID

		// 1. 如果发送者是超级管理员，始终无条件放行
		if h.isAdmin(c) {
			return next(c)
		}

		// 2. 私聊场景校验发送者授权；群组/频道场景必须校验该群会话(chatID)本身已被管理员授权
		if c.Chat().Type == tele.ChatPrivate {
			if h.engine.IsAuthorized(senderID) {
				return next(c)
			}
		} else {
			if h.engine.IsAuthorized(chatID) {
				return next(c)
			}
		}

		// 3. 内联按钮点击提示
		if c.Callback() != nil {
			return c.Respond(&tele.CallbackResponse{
				Text:      "⛔ 访问受限：当前未在白名单授权列表中",
				ShowAlert: true,
			})
		}

		// 4. 针对普通消息或指令，返回未授权提示并展示自身 ID
		idType := "个人 Telegram ID"
		displayID := senderID
		if c.Chat().Type != tele.ChatPrivate {
			idType = "群组 Chat ID"
			displayID = chatID
		}

		text := fmt.Sprintf("⛔ <b>【未授权访问】</b>\n\n"+
			"• 您的 %s: <code>%d</code>\n"+
			"• 运行模式: 私密白名单制监控服务\n\n"+
			"💡 <i>如需使用，请将上方 ID 复制发送给管理员申请开通权限。</i>", idType, displayID)

		return c.Send(text, tele.ModeHTML)
	}
}

// hasPermission 校验当前发送者是否有权修改监控设置
func (h *Handler) hasPermission(c tele.Context) bool {
	senderID := c.Sender().ID
	chatID := c.Chat().ID

	// 1. 超级管理员始终拥有最高控制权
	if h.isAdmin(c) {
		return true
	}

	// 2. 私聊场景：白名单用户对其个人专属会话拥有 100% 自主配置权
	if c.Chat().Type == tele.ChatPrivate {
		return h.engine.IsAuthorized(chatID) || h.engine.IsAuthorized(senderID)
	}

	// 3. 群组场景：仅允许群管理员或群主进行设置调整
	admins, err := c.Bot().AdminsOf(c.Chat())
	if err != nil {
		return false // 无法获取管理员列表时严格拒绝 (Fail-Closed)
	}
	for _, admin := range admins {
		if admin.User != nil && admin.User.ID == senderID {
			return true
		}
	}
	return false
}

func (h *Handler) isAdmin(c tele.Context) bool {
	return h.adminID != 0 && c.Sender().ID == h.adminID
}

func (h *Handler) registerAdminCommands(b *tele.Bot, adminID int64) {
	if adminID == 0 {
		return
	}
	adminCommands := []tele.Command{
		{Text: "check", Description: "立即查询当前在售库存与命中情况"},
		{Text: "menu", Description: "打开内联交互式过滤菜单面板"},
		{Text: "filter", Description: "查看或配置高级正则过滤规则"},
		{Text: "sub", Description: "开关通知推送 (/sub on|off)"},
		{Text: "mute", Description: "开启临时免打扰 (/mute 1h)"},
		{Text: "id", Description: "查看当前会话的 Chat ID"},
		{Text: "status", Description: "查看监控服务运行状态"},
		{Text: "help", Description: "查看命令语法与使用教程"},
		{Text: "admin", Description: "👑 [管理] 打开管理员控制面板"},
		{Text: "user", Description: "👑 [管理] 白名单授权管理 (/user add|del|list)"},
		{Text: "stats", Description: "👑 [管理] 查看系统监控大盘统计"},
		{Text: "broadcast", Description: "👑 [管理] 向全员群发系统维护广播"},
	}
	_ = b.SetCommands(adminCommands, tele.CommandScope{Type: tele.CommandScopeChat, ChatID: adminID})
}

// HandleStart /start 命令欢迎与面板展示
func (h *Handler) HandleStart(c tele.Context) error {
	chatID := c.Chat().ID
	if c.Chat().Type != tele.ChatPrivate && !h.hasPermission(c) {
		return c.Send("⚠️ 仅管理员或群主有权在群组内执行 /start 初始化设置。")
	}

	// 确保当前会话已激活并加入推送列表（仅对已授权会话生效）
	h.engine.EnsureChatSubscribed(chatID)

	// 若为管理员，动态为其 Telegram 客户端激活管理员特权菜单
	if h.isAdmin(c) {
		go h.registerAdminCommands(c.Bot(), c.Sender().ID)
	}

	cfg := h.engine.GetChatConfig(chatID)

	text := "👋 欢迎使用 <b>Narwhal Cloud 专属监控管家</b>！\n\n" +
		fmt.Sprintf("🆔 <b>当前会话 Chat ID:</b> <code>%d</code> <i>(已自动开启推送)</i>\n", chatID) +
		"本 Bot 专为 VPS 实时监控设计，补货秒级推送到位。\n" +
		"您可以随时使用下方菜单或命令自定义过滤规则，<b>专属独立隔离，即设即生效</b>。\n\n" +
		RenderSettingsText(cfg)

	markup := BuildMenuKeyboard(c.Bot(), cfg)
	return c.Send(text, markup, tele.ModeHTML)
}

// HandleCheck /check 立即扫描并返回当前符合条件的在售套餐（优先读取内存快照，0ms 响应且防限速）
func (h *Handler) HandleCheck(c tele.Context) error {
	if h.apiClient == nil {
		return c.Send("❌ API 客户端未就绪")
	}

	plans, cacheTime := h.apiClient.GetCachedPlans()
	if len(plans) == 0 {
		h.checkMu.Lock()
		// 双重检查锁定：若并发请求排队期间前序请求已完成拉取并建立缓存，直接复用
		plans, cacheTime = h.apiClient.GetCachedPlans()
		if len(plans) == 0 {
			if time.Since(h.lastDirectPoll) < 10*time.Second {
				h.checkMu.Unlock()
				return c.Send("⏳ 平台库存数据初始化同步中，请等待几秒后再试。")
			}
			h.lastDirectPoll = time.Now()

			msgWait, _ := c.Bot().Send(c.Chat(), "🔍 正在扫描 Narwhal 平台最新在售库存...", tele.ModeHTML)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			var err error
			plans, err = h.apiClient.GetPlans(ctx)
			cancel()
			if msgWait != nil {
				_ = c.Bot().Delete(msgWait)
			}
			if err != nil {
				h.checkMu.Unlock()
				return c.Send(fmt.Sprintf("❌ 获取库存失败: %v", err))
			}
			cacheTime = time.Now()
		}
		h.checkMu.Unlock()
	}

	chatID := c.Chat().ID

	// 统计地区库存与符合条件的套餐
	regionStock := make(map[string]int)
	var matchedPlans []api.PublicPlan

	for _, p := range plans {
		if p.SoldOut || p.RamInsufficient {
			continue
		}
		regionStock[strings.ToUpper(p.MachineRegion)]++

		fullDesc := p.Description
		if p.MachineDescription != "" {
			if fullDesc != "" {
				fullDesc += " " + p.MachineDescription
			} else {
				fullDesc = p.MachineDescription
			}
		}

		payload := filter.ItemPayload{
			Type:        "plan",
			ID:          p.ID,
			Title:       p.Name,
			Description: fullDesc,
			Region:      p.MachineRegion,
			Price:       p.PriceMonthly,
			CPU:         p.CPU,
			RamMB:       p.RamMB,
			DiskGB:      p.DiskGB,
			Bandwidth:   p.BandwidthMbps,
			MachineName: p.MachineName,
			Tags:        p.MachineTags,
			Remaining:   p.Remaining,
		}

		if h.engine.Evaluate(chatID, &payload) {
			matchedPlans = append(matchedPlans, p)
		}
	}

	var sb strings.Builder
	sb.WriteString("📊 <b>【Narwhal Cloud 实时库存状态简报】</b>\n\n")

	// 地区总览
	sb.WriteString("🌍 <b>当前在售机房分布:</b>\n")
	if len(regionStock) == 0 {
		sb.WriteString("  <i>(暂无任何在售机器或全部售罄)</i>\n")
	} else {
		var regions []string
		for reg := range regionStock {
			regions = append(regions, reg)
		}
		sort.Strings(regions)
		for _, reg := range regions {
			flag := GetRegionFlag(reg)
			name := GetRegionName(reg)
			sb.WriteString(fmt.Sprintf("  • %s %s (%s): <b>%d</b> 款有货\n", flag, html.EscapeString(name), html.EscapeString(reg), regionStock[reg]))
		}
	}

	sb.WriteString("\n🎯 <b>命中当前过滤规则的可用套餐:</b>\n")
	if len(matchedPlans) == 0 {
		sb.WriteString("  <i>当前没有符合您过滤条件的在售套餐（或已全部售罄）。补货时将第一时间推送到此！</i>\n")
	} else {
		// 最多列出 6 款，避免刷屏
		for i, p := range matchedPlans {
			if i >= 6 {
				sb.WriteString(fmt.Sprintf("  <i>... 另外还有 %d 款未展示</i>\n", len(matchedPlans)-6))
				break
			}
			stockStr := "充足"
			if p.Remaining > 0 {
				stockStr = fmt.Sprintf("仅剩 %d 台", p.Remaining)
			}
			regFlag := GetRegionFlag(p.MachineRegion)
			sb.WriteString(fmt.Sprintf("  • %s <b>%s</b> | $%.2f/月\n    规格: %dc/%dM/%dG | 库存: <code>%s</code>\n",
				regFlag, html.EscapeString(p.Name), p.PriceMonthly, p.CPU, p.RamMB, p.DiskGB, stockStr))
		}
	}

	sb.WriteString(fmt.Sprintf("\n💡 <i>输入 /menu 可调整过滤规则，或使用 /filter regex 过滤关键词</i>\n"))
	if !cacheTime.IsZero() {
		sb.WriteString(fmt.Sprintf("⏰ <i>快照时间: %s (内存瞬时检索)</i>", cacheTime.Format("15:04:05")))
	}

	return c.Send(sb.String(), tele.ModeHTML)
}

// HandleHelp /help 使用帮助
// HandleHelp /help 使用帮助
func (h *Handler) HandleHelp(c tele.Context) error {
	var sb strings.Builder
	sb.WriteString(`📖 <b>【Narwhal Monitor 命令与正则过滤使用指南】</b>

<b>1. 基础与监控</b>
• <code>/check</code> - 立即查询平台当前在售库存与规则命中情况
• <code>/menu</code> - 调出交互式按钮控制台（点按切换地区/限价）
• <code>/id</code> - 查看当前私聊或群聊的 Chat ID

<b>2. 规则快速过滤命令（即设即生效）</b>
• <code>/filter list</code> - 查看当前已生效的规则列表
• <code>/filter add [条件...]</code> - 添加复合过滤规则
• <code>/filter regex &lt;正则表达式&gt;</code> - 快速添加正向正则过滤（满足才推）
• <code>/filter exclude &lt;正则表达式&gt;</code> - 快速添加反向正则排除（满足则丢弃）
• <code>/filter del &lt;规则ID&gt;</code> - 删除指定规则
• <code>/filter clear</code> - 清空所有自定义规则

<b>3. 复合规则语法示例：</b>
• <code>/filter add price&lt;=5 region=HK,JP</code> (月付≤$5 且位于香港或日本)
• <code>/filter add ram&gt;=1024 cpu&gt;=2</code> (内存≥1G 且 核心≥2)
• <code>/filter add regex=(?i)cn2|cmi|精品</code> (匹配包含精品线路的套餐)
• <code>/filter add exclude_regex=(?i)nat|ipv6-only</code> (排除 NAT 机器)
• <code>/filter add price&lt;=3.5 region=HK regex=(?i)bgp</code> (多条件组合)

<b>4. 通知开关与免打扰</b>
• <code>/sub on</code> - 开启本会话通知推送
• <code>/sub off</code> - 暂停通知推送
• <code>/mute 2h</code> - 临时免打扰 2 小时 (支持 30m, 1h, 6h 等)`)

	if h.isAdmin(c) {
		sb.WriteString("\n\n<b>5. 👑 超级管理员特权指令</b>\n" +
			"• <code>/user add &lt;ID&gt; [备注]</code> - 授权新用户或群组并激活订阅\n" +
			"• <code>/user del &lt;ID&gt;</code> - 移除白名单授权并停用推送\n" +
			"• <code>/user list</code> - 查看完整白名单列表与规则状态\n" +
			"• <code>/stats</code> - 查看系统监控服务运行大盘\n" +
			"• <code>/broadcast &lt;内容&gt;</code> - 向全员群发系统维护广播")
	}

	return c.Send(sb.String(), tele.ModeHTML)
}

// HandleMenu /menu 打开内联菜单
func (h *Handler) HandleMenu(c tele.Context) error {
	if c.Chat().Type != tele.ChatPrivate && !h.hasPermission(c) {
		return c.Send("⚠️ 仅管理员或群主有权调出群组配置面板。")
	}
	chatID := c.Chat().ID
	cfg := h.engine.GetChatConfig(chatID)
	text := RenderSettingsText(cfg)
	markup := BuildMenuKeyboard(c.Bot(), cfg)
	return c.Send(text, markup, tele.ModeHTML)
}

// HandleFilter /filter 命令路由分发
func (h *Handler) HandleFilter(c tele.Context) error {
	args := strings.Fields(c.Text())
	chatID := c.Chat().ID

	// 无参数时直接打开内联菜单
	if len(args) <= 1 {
		return h.HandleMenu(c)
	}

	subCmd := strings.ToLower(args[1])
	switch subCmd {
	case "list":
		return h.listRules(c, chatID)
	case "clear":
		if !h.hasPermission(c) {
			return c.Send("⚠️ 权限不足: 仅管理员或群主有权清空过滤规则。")
		}
		h.engine.ClearRules(chatID)
		return c.Send("🧹 已清空当前所有高级过滤规则，恢复默认全量推送状态！")
	case "del", "rm", "delete":
		if !h.hasPermission(c) {
			return c.Send("⚠️ 权限不足: 仅管理员或群主有权删除过滤规则。")
		}
		if len(args) < 3 {
			return c.Send("❌ 用法错误: 请输入要删除的规则 ID，例如: <code>/filter del r1001</code>", tele.ModeHTML)
		}
		ruleID := args[2]
		if h.engine.DeleteRule(chatID, ruleID) {
			return c.Send(fmt.Sprintf("✅ 规则 <code>%s</code> 已删除并即时失效！", html.EscapeString(ruleID)), tele.ModeHTML)
		}
		return c.Send(fmt.Sprintf("❌ 未找到 ID 为 <code>%s</code> 的规则，请使用 <code>/filter list</code> 查看有效 ID。", html.EscapeString(ruleID)), tele.ModeHTML)
	case "regex":
		if !h.hasPermission(c) {
			return c.Send("⚠️ 权限不足: 仅管理员或群主有权添加正则过滤。")
		}
		if len(args) < 3 {
			return c.Send("❌ 用法错误: <code>/filter regex &lt;表达式&gt;</code>\n例如: <code>/filter regex (?i)cn2|香港|hk</code>", tele.ModeHTML)
		}
		rawRegex := strings.Join(args[2:], " ")
		return h.addSimpleRegexRule(c, chatID, rawRegex, false)
	case "exclude":
		if !h.hasPermission(c) {
			return c.Send("⚠️ 权限不足: 仅管理员或群主有权添加排除正则。")
		}
		if len(args) < 3 {
			return c.Send("❌ 用法错误: <code>/filter exclude &lt;表达式&gt;</code>\n例如: <code>/filter exclude (?i)nat|ipv6-only</code>", tele.ModeHTML)
		}
		rawRegex := strings.Join(args[2:], " ")
		return h.addSimpleRegexRule(c, chatID, rawRegex, true)
	case "add":
		if !h.hasPermission(c) {
			return c.Send("⚠️ 权限不足: 仅管理员或群主有权添加规则。")
		}
		return h.parseAndAddRule(c, chatID, args[2:])
	default:
		return c.Send("❌ 未知子命令。输入 <code>/help</code> 查看详细用法。", tele.ModeHTML)
	}
}

func (h *Handler) listRules(c tele.Context, chatID int64) error {
	cfg := h.engine.GetChatConfig(chatID)
	if len(cfg.Rules) == 0 {
		return c.Send("📋 当前没有配置任何高级过滤规则，系统按照基础菜单设置推送。\n输入 <code>/help</code> 可查看如何添加规则。", tele.ModeHTML)
	}

	var sb strings.Builder
	sb.WriteString("📋 <b>【当前生效的高级过滤规则列表】</b>\n\n")

	for i, r := range cfg.Rules {
		sb.WriteString(fmt.Sprintf("<b>#%d [ID: <code>%s</code>]</b>\n", i+1, r.ID))
		if r.MaxPrice > 0 {
			sb.WriteString(fmt.Sprintf("  • 价格上限: ≤ $%.2f\n", r.MaxPrice))
		}
		if r.MinPrice > 0 {
			sb.WriteString(fmt.Sprintf("  • 价格下限: ≥ $%.2f\n", r.MinPrice))
		}
		if len(r.Regions) > 0 {
			sb.WriteString(fmt.Sprintf("  • 限定地区: <code>%s</code>\n", strings.Join(r.Regions, ", ")))
		}
		if r.MinCPU > 0 {
			sb.WriteString(fmt.Sprintf("  • 核心下限: ≥ %d 核\n", r.MinCPU))
		}
		if r.MinRAM > 0 {
			sb.WriteString(fmt.Sprintf("  • 内存下限: ≥ %d MB\n", r.MinRAM))
		}
		if r.Regex != "" {
			sb.WriteString(fmt.Sprintf("  • 正向正则: <code>%s</code>\n", html.EscapeString(r.Regex)))
		}
		if r.ExcludeRegex != "" {
			sb.WriteString(fmt.Sprintf("  • 排除正则: <code>%s</code>\n", html.EscapeString(r.ExcludeRegex)))
		}
		sb.WriteString(fmt.Sprintf("  <i>(删除此规则: /filter del %s)</i>\n\n", r.ID))
	}

	return c.Send(sb.String(), tele.ModeHTML)
}

func (h *Handler) addSimpleRegexRule(c tele.Context, chatID int64, rawRegex string, isExclude bool) error {
	// 先行验证正则合法性
	if _, err := regexp.Compile(rawRegex); err != nil {
		return c.Send(fmt.Sprintf("❌ <b>正则表达式语法错误</b>:\n<code>%s</code>", html.EscapeString(err.Error())), tele.ModeHTML)
	}

	rule := filter.Rule{}
	desc := ""
	if isExclude {
		rule.ExcludeRegex = rawRegex
		desc = fmt.Sprintf("反向排除正则 [<code>%s</code>]", html.EscapeString(rawRegex))
	} else {
		rule.Regex = rawRegex
		desc = fmt.Sprintf("正向匹配正则 [<code>%s</code>]", html.EscapeString(rawRegex))
	}

	cr, err := h.engine.AddRule(chatID, rule)
	if err != nil {
		return c.Send(fmt.Sprintf("❌ 添加失败: %s", html.EscapeString(err.Error())), tele.ModeHTML)
	}

	return c.Send(fmt.Sprintf("✅ <b>规则添加成功并实时生效！</b>\n\n• 规则 ID: <code>%s</code>\n• 描述: %s\n• 删除请用: <code>/filter del %s</code>", cr.ID, desc, cr.ID), tele.ModeHTML)
}

func (h *Handler) parseAndAddRule(c tele.Context, chatID int64, tokens []string) error {
	if len(tokens) == 0 {
		return c.Send("❌ 请提供过滤参数，例如: <code>/filter add price&lt;=5 region=HK,JP regex=cn2</code>", tele.ModeHTML)
	}

	var rule filter.Rule

	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}

		if strings.Contains(token, "<=") {
			parts := strings.SplitN(token, "<=", 2)
			key, val := strings.ToLower(parts[0]), parts[1]
			if key == "price" || key == "max_price" {
				if f, err := strconv.ParseFloat(val, 64); err == nil {
					rule.MaxPrice = f
				}
			}
		} else if strings.Contains(token, ">=") {
			parts := strings.SplitN(token, ">=", 2)
			key, val := strings.ToLower(parts[0]), parts[1]
			if key == "price" || key == "min_price" {
				if f, err := strconv.ParseFloat(val, 64); err == nil {
					rule.MinPrice = f
				}
			} else if key == "cpu" {
				if i, err := strconv.Atoi(val); err == nil {
					rule.MinCPU = i
				}
			} else if key == "ram" {
				if i, err := strconv.Atoi(val); err == nil {
					rule.MinRAM = i
				}
			}
		} else if strings.Contains(token, "=") {
			parts := strings.SplitN(token, "=", 2)
			key, val := strings.ToLower(parts[0]), parts[1]
			switch key {
			case "region", "regions":
				regs := strings.Split(val, ",")
				for _, r := range regs {
					r = strings.TrimSpace(r)
					if r != "" {
						rule.Regions = append(rule.Regions, strings.ToUpper(r))
					}
				}
			case "price", "max_price":
				if f, err := strconv.ParseFloat(val, 64); err == nil {
					rule.MaxPrice = f
				}
			case "min_price":
				if f, err := strconv.ParseFloat(val, 64); err == nil {
					rule.MinPrice = f
				}
			case "cpu":
				if i, err := strconv.Atoi(val); err == nil {
					rule.MinCPU = i
				}
			case "ram":
				if i, err := strconv.Atoi(val); err == nil {
					rule.MinRAM = i
				}
			case "regex":
				rule.Regex = val
			case "exclude_regex", "exclude":
				rule.ExcludeRegex = val
			}
		}
	}

	cr, err := h.engine.AddRule(chatID, rule)
	if err != nil {
		return c.Send(fmt.Sprintf("❌ 添加规则失败: %s", html.EscapeString(err.Error())), tele.ModeHTML)
	}

	return c.Send(fmt.Sprintf("✅ <b>复合过滤规则添加成功并实时生效！</b>\n\n• 规则 ID: <code>%s</code>\n• 可通过 <code>/filter list</code> 查看，或 <code>/filter del %s</code> 删除", cr.ID, cr.ID), tele.ModeHTML)
}

// HandleSub /sub 命令切换全局推送开关
func (h *Handler) HandleSub(c tele.Context) error {
	if !h.hasPermission(c) {
		return c.Send("⚠️ 权限不足: 仅管理员或群主有权调整通知推送开关。")
	}

	args := strings.Fields(c.Text())
	chatID := c.Chat().ID

	if len(args) <= 1 {
		cfg := h.engine.GetChatConfig(chatID)
		status := "🟢 开启中"
		if !cfg.Subscribed {
			status = "🔴 已关闭"
		}
		return c.Send(fmt.Sprintf("当前通知推送状态: %s\n用法: <code>/sub on</code> 开启，<code>/sub off</code> 关闭", status), tele.ModeHTML)
	}

	action := strings.ToLower(args[1])
	if action == "on" || action == "true" || action == "enable" {
		h.engine.SetSubscribed(chatID, true)
		return c.Send("✅ 已开启通知推送！有满足条件的新事件将第一时间推送。")
	} else if action == "off" || action == "false" || action == "disable" {
		h.engine.SetSubscribed(chatID, false)
		return c.Send("🔕 已暂停通知推送！在重新输入 <code>/sub on</code> 前将不会接收任何消息。", tele.ModeHTML)
	}

	return c.Send("❌ 参数错误，请输入 <code>/sub on</code> 或 <code>/sub off</code>", tele.ModeHTML)
}

// HandleMute /mute 临时免打扰
func (h *Handler) HandleMute(c tele.Context) error {
	if !h.hasPermission(c) {
		return c.Send("⚠️ 权限不足: 仅管理员或群主有权设置免打扰。")
	}

	args := strings.Fields(c.Text())
	chatID := c.Chat().ID

	if len(args) <= 1 {
		return c.Send("用法: <code>/mute 1h</code> (免打扰1小时)，或 <code>/mute 0</code> (解除静音)", tele.ModeHTML)
	}

	durationStr := args[1]
	if durationStr == "0" || durationStr == "off" {
		h.engine.SetMute(chatID, 0)
		return c.Send("🔔 已解除静音，恢复正常接收通知！")
	}

	d, err := time.ParseDuration(durationStr)
	if err != nil {
		return c.Send("❌ 时长格式不合法，支持如: <code>30m</code>, <code>1h</code>, <code>2h</code>, <code>24h</code>", tele.ModeHTML)
	}

	until := h.engine.SetMute(chatID, d)
	return c.Send(fmt.Sprintf("🔕 已开启临时免打扰，直到 <b>%s</b> 为止（持续 %v）。", until.Format("2006-01-02 15:04:05"), d), tele.ModeHTML)
}

// HandleAdmin /admin 超级管理员主控制台
func (h *Handler) HandleAdmin(c tele.Context) error {
	if !h.isAdmin(c) {
		return c.Send("⚠️ 仅超级管理员有权访问管理控制台。")
	}
	if c.Chat().Type != tele.ChatPrivate {
		return c.Send("⚠️ 管理控制台仅限在与 Bot 的私聊中打开，防止控制按钮在群组中暴露。")
	}

	// 动态激活管理员特权菜单
	go h.registerAdminCommands(c.Bot(), c.Sender().ID)

	auths := h.engine.GetAllAuthorizedChats()
	subs := h.engine.GetAllSubscribedChats()

	text := fmt.Sprintf("👑 <b>【Narwhal Monitor 超级管理员控制台】</b>\n\n"+
		"• <b>当前白名单:</b> 共 <b>%d</b> 个授权会话 (推送中: %d 个)\n"+
		"• <b>超级管理员:</b> <code>%d</code> (您自身)\n\n"+
		"<b>🛠️ 管理指令速查:</b>\n"+
		"• <code>/user add &lt;ID&gt; [备注]</code> - 授权新用户或群组并激活订阅\n"+
		"• <code>/user del &lt;ID&gt;</code> - 移除白名单授权并停用推送\n"+
		"• <code>/user list</code> - 查看完整白名单列表与规则状态\n"+
		"• <code>/stats</code> - 查看系统监控服务运行大盘 (内存/协程/运行时长)\n"+
		"• <code>/broadcast &lt;内容&gt;</code> - 向全员群发系统维护广播\n\n"+
		"<i>💡 您可以直接点击下方快捷按钮进行操作：</i>",
		len(auths), len(subs), h.adminID)

	menu := &tele.ReplyMarkup{}
	btnList := menu.Data("📋 查看白名单列表", "btn_admin_user_list")
	btnStats := menu.Data("📊 查看运行大盘", "btn_admin_stats")
	menu.Inline(
		menu.Row(btnList, btnStats),
	)

	return c.Send(text, menu, tele.ModeHTML)
}

// HandleUser 管理员专属白名单用户管理指令
func (h *Handler) HandleUser(c tele.Context) error {
	if !h.isAdmin(c) {
		return c.Send("⚠️ 仅超级管理员有权管理白名单用户。")
	}
	if c.Chat().Type != tele.ChatPrivate {
		return c.Send("⚠️ 白名单管理指令包含用户隐私，仅限在与 Bot 的私聊中执行。")
	}

	args := strings.Fields(c.Text())
	if len(args) <= 1 {
		usage := `📖 <b>【白名单用户管理指南】</b> (超级管理员专属)

• <code>/user add &lt;ID&gt; [备注]</code> - 将用户或群组加入白名单并激活订阅
• <code>/user del &lt;ID&gt;</code> - 移除授权并停用通知推送
• <code>/user list</code> - 查看当前完整授权白名单大盘`
		return c.Send(usage, tele.ModeHTML)
	}

	subCmd := strings.ToLower(args[1])
	switch subCmd {
	case "add":
		if len(args) < 3 {
			return c.Send("❌ 用法错误: <code>/user add &lt;ID&gt; [备注]</code>\n例如: <code>/user add 123456789 张三</code>", tele.ModeHTML)
		}
		targetID, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			return c.Send("❌ 用户 ID 必须为合法数字", tele.ModeHTML)
		}
		remark := ""
		if len(args) >= 4 {
			remark = strings.Join(args[3:], " ")
		}
		h.engine.AuthorizeChat(targetID, remark)

		// 若为私聊用户 (ID > 0)，尝试主动私发开通问候
		if targetID > 0 {
			go func() {
				_, _ = c.Bot().Send(&tele.Chat{ID: targetID},
					"🎉 <b>管理员已为您开通 Narwhal Cloud 监控使用权限！</b>\n\n"+
						"• 官方交互菜单: 输入 <code>/menu</code> 即可定制您的专属地区与价格\n"+
						"• 查看实时库存: 输入 <code>/check</code> 查看当前符合偏好的机器\n"+
						"• 过滤配置帮助: 输入 <code>/help</code> 查看规则编写指引\n\n"+
						"<i>💡 您的专属监控已自动开启，有符合您偏好的补货将在此第一时间推送！</i>", tele.ModeHTML)
			}()
		}

		remarkDesc := ""
		if remark != "" {
			remarkDesc = fmt.Sprintf(" (备注: %s)", html.EscapeString(remark))
		}
		return c.Send(fmt.Sprintf("✅ <b>已成功将 <code>%d</code>%s 加入授权白名单！</b>\n• 推送状态: 已自动激活\n• 专属配置: 用户可私聊自由配置地区与规则", targetID, remarkDesc), tele.ModeHTML)

	case "del", "rm", "delete":
		if len(args) < 3 {
			return c.Send("❌ 用法错误: <code>/user del &lt;ID&gt;</code>\n例如: <code>/user del 123456789</code>", tele.ModeHTML)
		}
		targetID, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			return c.Send("❌ 用户 ID 必须为合法数字", tele.ModeHTML)
		}
		if targetID == h.adminID {
			return c.Send("⚠️ 不能将超级管理员自身移出白名单！", tele.ModeHTML)
		}
		if h.engine.RevokeChat(targetID) {
			return c.Send(fmt.Sprintf("✅ 已成功将 <code>%d</code> 移出白名单并停用通知推送！", targetID), tele.ModeHTML)
		}
		return c.Send(fmt.Sprintf("❌ 未找到 ID 为 <code>%d</code> 的白名单用户。", targetID), tele.ModeHTML)

	case "list":
		auths := h.engine.GetAllAuthorizedChats()
		if len(auths) == 0 {
			return c.Send("📋 当前白名单列表为空。", tele.ModeHTML)
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("📋 <b>【当前授权白名单列表 (共 %d 个)】</b>\n\n", len(auths)))

		for i, cfg := range auths {
			roleIcon := "👤"
			if cfg.ChatID == h.adminID {
				roleIcon = "👑"
			} else if cfg.ChatID < 0 {
				roleIcon = "👥"
			}

			statusStr := "🔔 接收中"
			if !cfg.Subscribed {
				statusStr = "🔕 已暂停"
			} else if !cfg.MutedUntil.IsZero() && time.Now().Before(cfg.MutedUntil) {
				statusStr = "🟡 静音中"
			}

			remarkStr := ""
			if cfg.Remark != "" {
				remarkStr = fmt.Sprintf(" (<b>%s</b>)", html.EscapeString(cfg.Remark))
			}

			sb.WriteString(fmt.Sprintf("<b>#%d</b> %s <code>%d</code>%s\n", i+1, roleIcon, cfg.ChatID, remarkStr))
			sb.WriteString(fmt.Sprintf("   ├ 状态: %s | 规则: %d 条\n", statusStr, len(cfg.Rules)))

			regStr := "全地区"
			if len(cfg.QuickRegions) > 0 {
				regStr = strings.Join(cfg.QuickRegions, ",")
			}
			priceStr := "不限价"
			if cfg.QuickMaxPrice > 0 {
				priceStr = fmt.Sprintf("≤$%.2f", cfg.QuickMaxPrice)
			}
			sb.WriteString(fmt.Sprintf("   └ 偏好: %s | %s\n\n", regStr, priceStr))
		}

		return c.Send(sb.String(), tele.ModeHTML)

	default:
		return c.Send("❌ 未知子命令。输入 <code>/user</code> 查看用法。", tele.ModeHTML)
	}
}

// HandleStats /stats 查看系统监控大盘统计（管理员专属）
func (h *Handler) HandleStats(c tele.Context) error {
	if !h.isAdmin(c) {
		return c.Send("⚠️ 仅超级管理员有权查看系统统计大盘。")
	}
	if c.Chat().Type != tele.ChatPrivate {
		return c.Send("⚠️ 运行大盘包含账户凭据与余额信息，仅限在与 Bot 的私聊中执行。")
	}

	auths := h.engine.GetAllAuthorizedChats()
	subs := h.engine.GetAllSubscribedChats()

	var userCount, groupCount int
	for _, cfg := range auths {
		if cfg.ChatID < 0 {
			groupCount++
		} else {
			userCount++
		}
	}

	uptime := time.Since(h.startTime).Truncate(time.Second)

	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	allocMB := float64(m.Alloc) / 1024 / 1024
	sysMB := float64(m.Sys) / 1024 / 1024

	var sb strings.Builder
	sb.WriteString("📊 <b>【Narwhal Monitor 服务运行大盘】</b>\n\n")
	sb.WriteString(fmt.Sprintf("• <b>系统运行时长:</b> %s\n", uptime))
	sb.WriteString(fmt.Sprintf("• <b>授权白名单:</b> 共 %d 个 (个人: %d | 群组: %d)\n", len(auths), userCount, groupCount))
	sb.WriteString(fmt.Sprintf("• <b>活跃推送订阅:</b> %d 个会话\n", len(subs)))
	sb.WriteString(fmt.Sprintf("• <b>内存占用:</b> 堆分配 %.2f MB | 系统占用 %.2f MB\n", allocMB, sysMB))
	sb.WriteString(fmt.Sprintf("• <b>Go 协程数量:</b> %d\n", runtime.NumGoroutine()))

	if h.apiClient != nil && h.apiClient.HasAuth() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if me, err := h.apiClient.GetMe(ctx); err == nil {
			sb.WriteString(fmt.Sprintf("\n🐳 <b>Narwhal 账号:</b> %s\n", me.Email))
			sb.WriteString(fmt.Sprintf("💰 <b>账号余额:</b> $%.2f | 角色: %s\n", me.AvailableBalance, me.Role))
		}
	}

	return c.Send(sb.String(), tele.ModeHTML)
}

// HandleBroadcast /broadcast <消息内容> 向所有白名单授权且订阅的会话群发通知
func (h *Handler) HandleBroadcast(c tele.Context) error {
	if !h.isAdmin(c) {
		return c.Send("⚠️ 仅超级管理员有权执行全员广播。")
	}
	if c.Chat().Type != tele.ChatPrivate {
		return c.Send("⚠️ 系统广播指令仅限在与 Bot 的私聊中触发执行。")
	}

	args := strings.Fields(c.Text())
	if len(args) < 2 {
		return c.Send("❌ 用法错误: <code>/broadcast &lt;广播内容&gt;</code>", tele.ModeHTML)
	}

	msgContent := strings.TrimSpace(c.Text()[len(args[0]):])
	subscribers := h.engine.GetAllSubscribedChats()
	if len(subscribers) == 0 {
		return c.Send("⚠️ 当前没有任何活跃订阅的会话。")
	}

	broadcastMsg := fmt.Sprintf("📢 <b>【系统全员广播】</b>\n\n%s\n\n⏰ <i>发布时间: %s</i>",
		html.EscapeString(msgContent),
		time.Now().Format("2006-01-02 15:04:05"))

	successCount := 0
	failCount := 0

	for _, chatID := range subscribers {
		_, err := c.Bot().Send(&tele.Chat{ID: chatID}, broadcastMsg, tele.ModeHTML)
		if err != nil {
			failCount++
		} else {
			successCount++
		}
		// 频率控制：间隔 40ms 发信，限制全局广播在 25 msg/s 内，彻底杜绝 Telegram 429 Flood Wait
		time.Sleep(40 * time.Millisecond)
	}

	return c.Send(fmt.Sprintf("✅ <b>广播发送完成！</b>\n• 成功送达: %d 个会话\n• 发送失败: %d 个会话", successCount, failCount), tele.ModeHTML)
}

// HandleID 查看当前 Chat ID
func (h *Handler) HandleID(c tele.Context) error {
	chatID := c.Chat().ID
	chatType := "私聊"
	if c.Chat().Type == tele.ChatGroup || c.Chat().Type == tele.ChatSuperGroup {
		chatType = "群组"
	} else if c.Chat().Type == tele.ChatChannel {
		chatType = "频道"
	}

	reply := fmt.Sprintf("🆔 <b>当前会话信息:</b>\n\n"+
		"• <b>类型:</b> %s\n"+
		"• <b>Chat ID:</b> <code>%d</code>\n\n"+
		"💡 <i>提示: 若需开通专属白名单，可将此 ID 发送给管理员使用 <code>/user add %d</code> 授权。</i>",
		chatType, chatID, chatID)

	return c.Send(reply, tele.ModeHTML)
}

// HandleStatus /status 监控与服务健康度
func (h *Handler) HandleStatus(c tele.Context) error {
	uptime := time.Since(h.startTime).Truncate(time.Second)
	allSubscribed := h.engine.GetAllSubscribedChats()

	authStatus := "访客公开模式 (未配置 API_KEY)"
	if h.apiClient != nil && h.apiClient.HasAuth() {
		authStatus = "🟢 已认证 (Authorization: Bearer)"
	}

	text := fmt.Sprintf("📊 <b>【Narwhal Monitor 系统运行状态】</b>\n\n"+
		"• <b>服务运行时间:</b> %s\n"+
		"• <b>当前活动订阅数:</b> %d 个 Chat\n"+
		"• <b>API 鉴权状态:</b> %s\n"+
		"• <b>监控轮询状态:</b> 🟢 正常轮询中 (周期 15s)\n"+
		"• <b>内存与性能:</b> 纯 Go 原生常驻，零 GC 压力极低占用\n\n"+
		"<i>输入 /check 可查询当前库存，输入 /menu 可管理过滤偏好</i>", uptime, len(allSubscribed), authStatus)

	return c.Send(text, tele.ModeHTML)
}

// ---- 内联按钮回调处理 ----

func (h *Handler) HandleBtnRegion(c tele.Context) error {
	if !h.hasPermission(c) {
		return c.Respond(&tele.CallbackResponse{Text: "⚠️ 仅管理员或群主有权更改地区设置", ShowAlert: true})
	}
	region := c.Data()
	chatID := c.Chat().ID
	h.engine.ToggleRegion(chatID, region)

	cfg := h.engine.GetChatConfig(chatID)
	text := RenderSettingsText(cfg)
	markup := BuildMenuKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnPrice(c tele.Context) error {
	if !h.hasPermission(c) {
		return c.Respond(&tele.CallbackResponse{Text: "⚠️ 仅管理员或群主有权更改限价设置", ShowAlert: true})
	}
	priceStr := c.Data()
	chatID := c.Chat().ID
	price, _ := strconv.ParseFloat(priceStr, 64)
	h.engine.SetQuickMaxPrice(chatID, price)

	cfg := h.engine.GetChatConfig(chatID)
	text := RenderSettingsText(cfg)
	markup := BuildMenuKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnSubToggle(c tele.Context) error {
	if !h.hasPermission(c) {
		return c.Respond(&tele.CallbackResponse{Text: "⚠️ 仅管理员或群主有权调整开关", ShowAlert: true})
	}
	chatID := c.Chat().ID
	cfg := h.engine.GetChatConfig(chatID)
	h.engine.SetSubscribed(chatID, !cfg.Subscribed)

	newCfg := h.engine.GetChatConfig(chatID)
	text := RenderSettingsText(newCfg)
	markup := BuildMenuKeyboard(c.Bot(), newCfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnRefresh(c tele.Context) error {
	if !h.hasPermission(c) {
		return c.Respond(&tele.CallbackResponse{Text: "⚠️ 仅管理员或群主有权刷新设置面板", ShowAlert: true})
	}
	_ = c.Respond()
	chatID := c.Chat().ID
	cfg := h.engine.GetChatConfig(chatID)
	text := RenderSettingsText(cfg)
	markup := BuildMenuKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnRareMenu(c tele.Context) error {
	if !h.hasPermission(c) {
		return c.Respond(&tele.CallbackResponse{Text: "⚠️ 仅管理员或群主有权更改地区设置", ShowAlert: true})
	}
	chatID := c.Chat().ID
	cfg := h.engine.GetChatConfig(chatID)
	text := RenderRareRegionsText(cfg)
	markup := BuildRareRegionsKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnRareToggle(c tele.Context) error {
	if !h.hasPermission(c) {
		return c.Respond(&tele.CallbackResponse{Text: "⚠️ 仅管理员或群主有权更改地区设置", ShowAlert: true})
	}
	reg := strings.ToUpper(strings.TrimSpace(c.Data()))
	chatID := c.Chat().ID
	h.engine.ToggleRegion(chatID, reg)

	cfg := h.engine.GetChatConfig(chatID)
	text := RenderRareRegionsText(cfg)
	markup := BuildRareRegionsKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnRareAll(c tele.Context) error {
	if !h.hasPermission(c) {
		return c.Respond(&tele.CallbackResponse{Text: "⚠️ 仅管理员或群主有权更改地区设置", ShowAlert: true})
	}
	chatID := c.Chat().ID
	// 一键开启全部地区 (设为空恢复系统默认全开模式)
	h.engine.SetRegions(chatID, nil)

	cfg := h.engine.GetChatConfig(chatID)
	text := RenderRareRegionsText(cfg)
	markup := BuildRareRegionsKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnRareClear(c tele.Context) error {
	if !h.hasPermission(c) {
		return c.Respond(&tele.CallbackResponse{Text: "⚠️ 仅管理员或群主有权更改地区设置", ShowAlert: true})
	}
	chatID := c.Chat().ID
	// 如果本来就是默认全开状态，清空冷门相当于只保留 9 大核心区
	cfg := h.engine.GetChatConfig(chatID)
	if len(cfg.QuickRegions) == 0 {
		h.engine.SetRegions(chatID, SupportedQuickRegions)
	} else {
		h.engine.DisableRegions(chatID, RareRegions)
	}

	newCfg := h.engine.GetChatConfig(chatID)
	text := RenderRareRegionsText(newCfg)
	markup := BuildRareRegionsKeyboard(c.Bot(), newCfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnMenuHome(c tele.Context) error {
	if !h.hasPermission(c) {
		return c.Respond(&tele.CallbackResponse{Text: "⚠️ 仅管理员或群主有权更改设置", ShowAlert: true})
	}
	chatID := c.Chat().ID
	cfg := h.engine.GetChatConfig(chatID)
	text := RenderSettingsText(cfg)
	markup := BuildMenuKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}
