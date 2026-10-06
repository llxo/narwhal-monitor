// Package bot - 统一门禁与权限路由守卫中间件
//
// 负责在路由入口集中拦截鉴权、日志审计与防刷流控，业务 Handler 无需重复编写权限判断：
// • LoggingMiddleware: 全局交互日志审计与群聊非命令消息降噪
// • RequireAdmin: 超级管理员权限守卫 (/admin, /user, /check, /status, /broadcast)
// • RequireMember: 正式白名单会员权限守卫 (/menu, /filter 及规则定制按钮)
// • RequireControlPermission: 订阅控制权限守卫 (/sub, /mute，允许正式白名单与有效游客)
package bot

import (
	"fmt"
	"log"
	"strings"

	tele "gopkg.in/telebot.v3"
)

// LoggingMiddleware 全局交互审计日志中间件
func (h *Handler) LoggingMiddleware(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if c.Chat() == nil {
			return nil
		}

		senderID := int64(0)
		senderName := "unknown"
		if c.Sender() != nil {
			senderID = c.Sender().ID
			senderName = c.Sender().Username
			if senderName == "" {
				senderName = c.Sender().FirstName
			}
		}
		chatID := c.Chat().ID

		inText := c.Text()
		if inText == "" && c.Callback() != nil {
			inText = "[Callback: " + c.Callback().Data + "]"
		}
		log.Printf("[收到交互] Chat: %d | 来自: %d (@%s) | 内容: %s", chatID, senderID, senderName, inText)

		// 群组场景防护：群内普通聊天文本（非 / 开头的指令）静默忽略，杜绝机器人无差别回复报错刷屏
		if c.Chat().Type != tele.ChatPrivate && !strings.HasPrefix(strings.TrimSpace(c.Text()), "/") && c.Callback() == nil {
			return nil
		}

		return next(c)
	}
}

// RequireAdmin 路由守卫：仅允许超级管理员访问
func (h *Handler) RequireAdmin(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if h.isAdmin(c) {
			return next(c)
		}

		senderID := int64(0)
		if c.Sender() != nil {
			senderID = c.Sender().ID
		}
		log.Printf("[权限拦截] 非管理员 %d 尝试访问管理员专属接口", senderID)
		if c.Callback() != nil {
			return c.Respond(&tele.CallbackResponse{
				Text:      "⚠️ 仅超级管理员有权操作",
				ShowAlert: true,
			})
		}
		return c.Send("⚠️ 仅超级管理员有权访问此管理功能。", tele.ModeHTML)
	}
}

// RequireMember 路由守卫：仅允许正式白名单会员与超级管理员访问（拦截游客与未授权访客）
func (h *Handler) RequireMember(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		chatID := c.Chat().ID
		senderID := int64(0)
		if c.Sender() != nil {
			senderID = c.Sender().ID
		}

		// 1. 群组场景鉴权：群组必须被加入授权白名单，且操作者必须具备管理权限
		if c.Chat().Type != tele.ChatPrivate {
			// 群组未被授权
			if !h.engine.IsAuthorized(chatID) {
				log.Printf("[权限拦截] 未授权群组 %d 尝试访问会员接口", chatID)
				if c.Callback() != nil {
					return c.Respond(&tele.CallbackResponse{
						Text:      "⛔ 当前群组未在授权白名单中",
						ShowAlert: true,
					})
				}
				text := fmt.Sprintf("⛔ <b>【群组未授权】</b>\n\n"+
					"• 群组 Chat ID: <code>%d</code>\n"+
					"• 运行模式: 白名单制监控服务\n\n"+
					"💡 <i>如需在群组中启用监控，请将上方群组 ID 复制发送给管理员申请开通。</i>", chatID)
				return c.Send(text, tele.ModeHTML)
			}

			// 群组已授权，但发言者/点击者并非群管理员
			if !h.hasPermission(c) {
				log.Printf("[权限拦截] 群组 %d 普通成员 %d 尝试操作会员接口", chatID, senderID)
				if c.Callback() != nil {
					return c.Respond(&tele.CallbackResponse{
						Text:      "⚠️ 权限不足: 仅群管理员或群主有权调整群监控设置",
						ShowAlert: true,
					})
				}
				return c.Send("⚠️ 权限不足: 仅群管理员或群主有权操作群监控配置。")
			}

			return next(c)
		}

		// 2. 私聊场景鉴权：仅校验个人会话白名单与超级管理员
		if h.isAdmin(c) || h.engine.IsAuthorized(chatID) {
			return next(c)
		}

		// 游客模式特权拦截提示
		if h.engine.IsGuest(chatID) {
			log.Printf("[权限拦截] 游客 %d 尝试访问正式会员高级定制接口已被拦截", senderID)
			if c.Callback() != nil {
				return c.Respond(&tele.CallbackResponse{
					Text:      "💡 游客模式无权使用高级定制，自选过滤仅对正式白名单开放",
					ShowAlert: true,
				})
			}
			return c.Send("💡 <b>您当前为【游客体验模式】</b>\n\n"+
				"• <b>推送通道:</b> 🟢 默认接收平台<b>全部未过滤</b>的上新与补货通知\n"+
				"• <b>高级定制:</b> 自选地区、限价与关键词过滤仅对【正式白名单会员】开放\n\n"+
				"<i>如需自选专属过滤规则，请联系管理员申请升级为正式白名单。</i>", tele.ModeHTML)
		}

		// 未授权外部访客拦截提示
		log.Printf("[权限拦截] 未授权用户 %d 访问会员接口", senderID)
		if c.Callback() != nil {
			return c.Respond(&tele.CallbackResponse{
				Text:      "⛔ 访问受限：当前未在授权列表中",
				ShowAlert: true,
			})
		}

		tipStr := "💡 <i>如需使用，请将上方 ID 复制发送给管理员申请开通权限。</i>"
		if h.engine.IsGuestModeEnabled() {
			tipStr = "💡 <i>当前系统已开放游客体验！可直接输入 <code>/register</code> 一键登记为游客，免费接收全量补货推送。</i>"
		}

		text := fmt.Sprintf("⛔ <b>【未授权访问】</b>\n\n"+
			"• 您的 个人 Telegram ID: <code>%d</code>\n"+
			"• 运行模式: 白名单制监控服务\n\n"+
			"%s", senderID, tipStr)

		return c.Send(text, tele.ModeHTML)
	}
}

// RequireControlPermission 路由守卫：用于 /sub 与 /mute（允许正式白名单与游客控制自身会话）
func (h *Handler) RequireControlPermission(next tele.HandlerFunc) tele.HandlerFunc {
	return func(c tele.Context) error {
		if h.hasControlPermission(c) {
			return next(c)
		}

		senderID := int64(0)
		if c.Sender() != nil {
			senderID = c.Sender().ID
		}
		log.Printf("[权限拦截] 用户 %d 无权调整通知推送开关", senderID)
		if c.Callback() != nil {
			return c.Respond(&tele.CallbackResponse{
				Text:      "⚠️ 权限不足: 仅管理员或群主有权调整开关",
				ShowAlert: true,
			})
		}
		return c.Send("⚠️ 权限不足: 仅管理员或群主有权调整通知推送开关。")
	}
}

// hasPermission 校验当前发送者是否有权修改监控设置 (仅限管理员与正式白名单)
func (h *Handler) hasPermission(c tele.Context) bool {
	if c.Sender() == nil {
		return false
	}
	senderID := c.Sender().ID
	chatID := c.Chat().ID

	// 1. 超级管理员始终拥有最高控制权
	if h.isAdmin(c) {
		return true
	}

	// 2. 私聊场景：白名单用户对其个人专属会话拥有 100% 自主配置权
	if c.Chat().Type == tele.ChatPrivate {
		return h.engine.IsAuthorized(chatID)
	}

	// 3. 群组场景：必须是已授权群组，且仅允许群管理员或群主进行设置调整
	if !h.engine.IsAuthorized(chatID) {
		return false
	}
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

// hasControlPermission 校验当前发送者是否有权调整基础通知开关与静音 (支持正式白名单与有效游客)
func (h *Handler) hasControlPermission(c tele.Context) bool {
	if h.isAdmin(c) {
		return true
	}
	chatID := c.Chat().ID

	// 私聊场景：正式白名单会员或游客均对其个人会话拥有启闭通知与静音的权利
	if c.Chat().Type == tele.ChatPrivate {
		return h.engine.IsAuthorized(chatID) || h.engine.IsGuest(chatID)
	}

	// 群组场景：必须是已授权会话且操作者为群管（群组不支持游客）
	return h.hasPermission(c)
}

func (h *Handler) isAdmin(c tele.Context) bool {
	if c.Sender() == nil || h.adminID == 0 {
		return false
	}
	return c.Sender().ID == h.adminID
}
