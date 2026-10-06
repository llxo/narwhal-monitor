// Package bot - 核心路由调度中心
//
// 负责声明式路由组装、中间件挂载与全局命令注册：
// • 🌐 公共基础组: /start, /help, /id, /register, /guest
// • 🔔 订阅通知组: /sub, /mute
// • 💎 白名单会员组: /menu, /filter (及自选地区/限价按钮)
// • 👑 超级管理员组: /admin, /user, /check, /status, /broadcast (及管理按钮)
package bot

import (
	"fmt"
	"html"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"narwhal-monitor/internal/api"
	"narwhal-monitor/internal/filter"
)

// Handler 集中管理 Telegram 命令路由分发与业务调度
type Handler struct {
	engine         *filter.Engine
	apiClient      *api.Client
	adminID        int64
	startTime      time.Time
	checkMu        sync.Mutex
	lastDirectPoll time.Time
	regMu          sync.Mutex
	regLast        map[int64]time.Time
}

// NewHandler 创建 Handler 实例
func NewHandler(engine *filter.Engine, apiClient *api.Client, adminID int64) *Handler {
	return &Handler{
		engine:    engine,
		apiClient: apiClient,
		adminID:   adminID,
		startTime: time.Now(),
		regLast:   make(map[int64]time.Time),
	}
}

// RegisterRoutes 统一挂载全局门禁中间件与声明式路由分组
func (h *Handler) RegisterRoutes(b *tele.Bot) {
	// 1. 全局交互审计与消息防护中间件
	b.Use(h.LoggingMiddleware)

	// 2. 🌐 公共基础组 (任何人均可访问，不设身份门槛)
	public := b.Group()
	public.Handle("/start", h.HandleStart)
	public.Handle("/help", h.HandleHelp)
	public.Handle("/id", h.HandleID)
	public.Handle("/register", h.HandleRegister)
	public.Handle("/guest", h.HandleGuest)

	// 3. 🔔 订阅通知控制组 (正式白名单与游客均可控制自身会话的推送与静音)
	subscriber := b.Group()
	subscriber.Use(h.RequireControlPermission)
	subscriber.Handle("/sub", h.HandleSub)
	subscriber.Handle("/mute", h.HandleMute)

	// 4. 💎 正式白名单会员专属组 (自选地区、限价、高级正则与内联交互按钮)
	member := b.Group()
	member.Use(h.RequireMember)
	member.Handle("/menu", h.HandleMenu)
	member.Handle("/filter", h.HandleFilter)

	// 白名单内联交互按钮
	member.Handle(&tele.Btn{Unique: "btn_reg"}, h.HandleBtnRegion)
	member.Handle(&tele.Btn{Unique: "btn_price"}, h.HandleBtnPrice)
	member.Handle(&tele.Btn{Unique: "btn_sub_toggle"}, h.HandleBtnSubToggle)
	member.Handle(&tele.Btn{Unique: "btn_refresh"}, h.HandleBtnRefresh)
	member.Handle(&tele.Btn{Unique: "btn_rare_menu"}, h.HandleBtnRareMenu)
	member.Handle(&tele.Btn{Unique: "btn_rare_toggle"}, h.HandleBtnRareToggle)
	member.Handle(&tele.Btn{Unique: "btn_rare_all"}, h.HandleBtnRareAll)
	member.Handle(&tele.Btn{Unique: "btn_rare_clear"}, h.HandleBtnRareClear)
	member.Handle(&tele.Btn{Unique: "btn_menu_home"}, h.HandleBtnMenuHome)

	// 5. 👑 超级管理员专属组 (系统状态、用户授权、库存扫描与全局广播)
	admin := b.Group()
	admin.Use(h.RequireAdmin)
	admin.Handle("/admin", h.HandleAdmin)
	admin.Handle("/user", h.HandleUser)
	admin.Handle("/check", h.HandleCheck)
	admin.Handle("/status", h.HandleStatus)
	admin.Handle("/broadcast", h.HandleBroadcast)

	// 管理员专属内联按钮
	admin.Handle(&tele.Btn{Unique: "btn_admin_menu"}, h.HandleAdmin)
	admin.Handle(&tele.Btn{Unique: "btn_admin_toggle_guest"}, h.HandleBtnToggleGuest)
	admin.Handle(&tele.Btn{Unique: "btn_admin_guests"}, h.HandleBtnAdminGuests)
	admin.Handle(&tele.Btn{Unique: "btn_admin_status"}, h.HandleStatus)
	admin.Handle(&tele.Btn{Unique: "btn_admin_stats"}, h.HandleStatus)
	admin.Handle(&tele.Btn{Unique: "btn_admin_user_list"}, func(c tele.Context) error {
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
				priceStr = fmt.Sprintf("≤%g$", cfg.QuickMaxPrice)
			}
			sb.WriteString(fmt.Sprintf("   └ 偏好: %s | %s\n\n", regStr, priceStr))
		}
		return c.Send(sb.String(), tele.ModeHTML)
	})
}

func (h *Handler) registerAdminCommands(b *tele.Bot, adminID int64) {
	if adminID == 0 {
		return
	}
	adminCommands := []tele.Command{
		{Text: "start", Description: "开启监控向导与欢迎信息"},
		{Text: "menu", Description: "打开交互式过滤菜单面板"},
		{Text: "filter", Description: "查看或配置高级正则过滤规则"},
		{Text: "sub", Description: "切换推送通知 (开启/暂停)"},
		{Text: "mute", Description: "开启临时免打扰 (/mute 1h)"},
		{Text: "id", Description: "查看当前会话的 Chat ID"},
		{Text: "help", Description: "查看命令语法与使用教程"},
		{Text: "admin", Description: "👑 [管理] 打开管理员控制面板"},
		{Text: "check", Description: "👑 [管理] 立即查询当前在售库存与命中"},
		{Text: "status", Description: "👑 [管理] 查看系统运行状态"},
		{Text: "user", Description: "👑 [管理] 白名单授权管理"},
		{Text: "guest", Description: "👑 [管理] 游客模式设置"},
		{Text: "broadcast", Description: "👑 [管理] 向全员群发系统维护广播"},
	}
	_ = b.SetCommands(adminCommands, tele.CommandScope{Type: tele.CommandScopeChat, ChatID: adminID})
}
