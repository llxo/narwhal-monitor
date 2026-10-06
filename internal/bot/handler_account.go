// Package bot - 用户账号体系与游客生命周期
//
// 负责处理游客模式注册与退出、游客控制台以及管理员对用户的授权管理：
// • /register - 快速登记加入游客体验模式
// • /guest    - 游客模式控制台（管理员展示管理面板，游客展示个人状态，访客引导开通）
// • /user     - 超级管理员专属用户管理 (/user add|del|upgrade|downgrade|list|guests)
package bot

import (
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"
)

// HandleRegister /register 快速登记加入游客体验模式
func (h *Handler) HandleRegister(c tele.Context) error {
	chatID := c.Chat().ID
	senderID := c.Sender().ID

	// 游客体验限制：仅限在与 Bot 的私聊中开通
	if c.Chat().Type != tele.ChatPrivate {
		return c.Send("⚠️ 游客体验模式仅限在与 Bot 的【个人私聊】中开通，群组会话暂不支持注册为游客。\n如需在群组中使用监控，请将群组 ID 发送给超级管理员开通专属白名单。", tele.ModeHTML)
	}

	// 1. 若是超级管理员
	if h.isAdmin(c) {
		return c.Send("👑 您是【超级管理员】，已拥有全量最高监控权限，无需登记为游客。", tele.ModeHTML)
	}

	// 2. 若是正式白名单
	if h.engine.IsAuthorized(chatID) || h.engine.IsAuthorized(senderID) {
		return c.Send("💎 您已拥有【正式白名单会员】权限，享有 39 地区自选与高级过滤特权，无需降级为游客。", tele.ModeHTML)
	}

	// 频控保护：避免用户短时间内高频狂刷
	h.regMu.Lock()
	last := h.regLast[senderID]
	if time.Since(last) < 3*time.Second {
		h.regMu.Unlock()
		return c.Send("⏳ 操作过于频繁，请稍候再试。")
	}
	h.regLast[senderID] = time.Now()
	h.regMu.Unlock()

	// 3. 执行游客注册
	userRemark := c.Sender().Username
	if userRemark == "" {
		userRemark = c.Sender().FirstName
	}
	cfg, isNew, err := h.engine.RegisterGuest(chatID, userRemark)
	if err != nil {
		log.Printf("[游客注册拒绝] 用户 %d (@%s) 注册未通过: %v", chatID, userRemark, err)
		return c.Send(fmt.Sprintf("⚠️ <b>注册未成功:</b> %s", html.EscapeString(err.Error())), tele.ModeHTML)
	}

	// 若已是游客且此前重复触发
	if !isNew {
		statusStr := "🟢 接收中"
		if !cfg.Subscribed {
			statusStr = "🔕 已暂停"
		}
		return c.Send(fmt.Sprintf("ℹ️ <b>您当前已是【Narwhal 游客用户】</b>\n\n"+
			"• <b>推送通道:</b> 🟢 默认激活（全量未过滤上新与补货）\n"+
			"• <b>推送状态:</b> %s\n"+
			"• <b>推送管理:</b> 发送 <code>/sub</code> 切换开启/暂停，发送 <code>/guest leave</code> 注销退出\n\n"+
			"<i>💡 游客模式为全量广播体验；自选地区与高级过滤仅对【正式白名单会员】开放。</i>", statusStr), tele.ModeHTML)
	}

	log.Printf("[游客注册成功] 用户 %d (@%s) 成功登记为新游客 (当前总游客: %d 人)", chatID, userRemark, h.engine.GetGuestCount())

	// 仅首次新注册时异步向超级管理员推送通知
	if h.adminID != 0 && chatID != h.adminID {
		go func() {
			guestCount := h.engine.GetGuestCount()
			maxGuests := h.engine.GetMaxGuests()
			limitDesc := "不限"
			if maxGuests > 0 {
				limitDesc = fmt.Sprintf("%d人", maxGuests)
			}
			adminNotify := fmt.Sprintf("🔔 <b>【新游客注册通知】</b>\n\n"+
				"• <b>用户 ID:</b> <code>%d</code> (@%s)\n"+
				"• <b>当前游客:</b> <b>%d</b> 人 (配额: %s)\n\n"+
				"<b>🛠️ 快捷管理指令:</b>\n"+
				"• 一键转正: <code>/user upgrade %d</code>\n"+
				"• 移除游客: <code>/user del %d</code>",
				chatID, html.EscapeString(userRemark), guestCount, limitDesc, chatID, chatID)
			_, _ = c.Bot().Send(&tele.Chat{ID: h.adminID}, adminNotify, tele.ModeHTML)
		}()
	}

	return c.Send("🎉 <b>恭喜！您已成功登记为【Narwhal 游客用户】</b>\n\n"+
		"• <b>推送通道:</b> 🟢 默认激活（接收平台<b>全部未过滤</b>的上新与补货通知）\n"+
		"• <b>推送管理:</b> 发送 <code>/sub</code> 即可一键开启/暂停，发送 <code>/guest leave</code> 可注销退出\n\n"+
		"<i>💡 游客模式为全量广播体验；自选地区与高级过滤仅对【正式白名单会员】开放。</i>", tele.ModeHTML)
}

// HandleGuest /guest 游客模式智能控制台与状态中心 (根据调用者身份自适应呈现)
func (h *Handler) HandleGuest(c tele.Context) error {
	// 游客控制台与管理面板仅限在私聊中操作，防止群聊泄露游客隐私与管理控制按钮
	if c.Chat().Type != tele.ChatPrivate {
		return c.Send("⚠️ 游客体验模式与管理控制台仅限在与 Bot 的【个人私聊】中进行操作。")
	}

	chatID := c.Chat().ID
	senderID := c.Sender().ID
	args := strings.Fields(c.Text())

	// ==========================================
	// 场景 1: 超级管理员操作与管理控制面板
	// ==========================================
	if h.isAdmin(c) {
		// 若带有子参数，智能解析管理命令
		if len(args) > 1 {
			sub := strings.ToLower(args[1])
			// 1.1 纯数字智能解析 (例如 /guest 20 代替 /guest limit 20)
			if num, err := strconv.Atoi(sub); err == nil && num >= 0 {
				h.engine.SetMaxGuests(num)
				limitStr := "不限制"
				if num > 0 {
					limitStr = fmt.Sprintf("%d 人", num)
				}
				log.Printf("[系统配置] 管理员 %d 调整游客名额上限为: %s", senderID, limitStr)
				return c.Send(fmt.Sprintf("✅ <b>游客限制名额已更新为:</b> <code>%s</code>", limitStr), tele.ModeHTML)
			}

			// 1.2 /guest limit <数字>
			if sub == "limit" {
				if len(args) < 3 {
					limit := h.engine.GetMaxGuests()
					limitStr := "不限制"
					if limit > 0 {
						limitStr = fmt.Sprintf("%d 人", limit)
					}
					return c.Send(fmt.Sprintf("当前游客名额限制: <b>%s</b>\n用法: <code>/guest limit &lt;数字&gt;</code> (0为不限制)", limitStr), tele.ModeHTML)
				}
				num, err := strconv.Atoi(args[2])
				if err != nil || num < 0 {
					return c.Send("❌ 限制人数必须为 >= 0 的整数", tele.ModeHTML)
				}
				h.engine.SetMaxGuests(num)
				limitStr := "不限制"
				if num > 0 {
					limitStr = fmt.Sprintf("%d 人", num)
				}
				log.Printf("[系统配置] 管理员 %d 调整游客名额上限为: %s", senderID, limitStr)
				return c.Send(fmt.Sprintf("✅ <b>游客限制名额已更新为:</b> <code>%s</code>", limitStr), tele.ModeHTML)
			}

			// 1.3 /guest on | open
			if sub == "on" || sub == "open" {
				h.engine.SetGuestModeEnabled(true)
				log.Printf("[系统配置] 管理员 %d 开启游客模式", senderID)
				return c.Send("✅ <b>游客模式已开启</b>，外部用户可通过 <code>/register</code> 自由注册。", tele.ModeHTML)
			}

			// 1.4 /guest off | close
			if sub == "off" || sub == "close" {
				h.engine.SetGuestModeEnabled(false)
				log.Printf("[系统配置] 管理员 %d 关闭游客模式", senderID)
				return c.Send("🛑 <b>游客模式已关闭</b>，已停止接收新游客注册。", tele.ModeHTML)
			}

			// 1.5 /guest list | guests (查看游客列表)
			if sub == "list" || sub == "guests" {
				return h.HandleBtnAdminGuests(c)
			}
		}

		// 管理员无参数输入 /guest 时，展示精美的【游客模式配置面板】与状态概览
		guestMode := h.engine.IsGuestModeEnabled()
		guestStatus := "🔴 已关闭 (暂停新用户登记)"
		if guestMode {
			guestStatus = "🟢 已开启 (对外开放体验)"
		}
		maxGuests := h.engine.GetMaxGuests()
		limitStr := "不限名额"
		if maxGuests > 0 {
			limitStr = fmt.Sprintf("%d 人", maxGuests)
		}
		guestCount := h.engine.GetGuestCount()

		remainingStr := "充足"
		if maxGuests > 0 {
			rem := maxGuests - guestCount
			if rem <= 0 {
				remainingStr = "已爆满 (0 人)"
			} else {
				remainingStr = fmt.Sprintf("剩余 %d 个", rem)
			}
		}

		adminPanelText := fmt.Sprintf("🌐 <b>【Narwhal Monitor 游客模式管理中心】</b>\n\n"+
			"• <b>通道开关:</b> %s\n"+
			"• <b>名额上限:</b> <b>%s</b>\n"+
			"• <b>活跃游客:</b> <b>%d</b> 人 (%s)\n\n"+
			"<b>🛠️ 管理指令速查:</b>\n"+
			"• <code>/guest on</code> - 开启游客体验注册通道\n"+
			"• <code>/guest off</code> - 关闭游客体验注册通道\n"+
			"• <code>/guest limit &lt;数量&gt;</code> - 设置限制名额 (亦可直接输入 <code>/guest &lt;数字&gt;</code>)\n"+
			"• <code>/user upgrade &lt;ID&gt;</code> - 一键将游客转为正式白名单\n"+
			"• <code>/user del &lt;ID&gt;</code> - 移除游客并停用推送\n\n"+
			"<i>💡 您可以直接点击下方快捷按钮进行操作：</i>",
			guestStatus, limitStr, guestCount, remainingStr)

		menu := &tele.ReplyMarkup{}
		btnToggle := menu.Data("🌐 切换开关状态", "btn_admin_toggle_guest")
		btnList := menu.Data("👥 查看游客列表", "btn_admin_guests")
		menu.Inline(
			menu.Row(btnToggle, btnList),
		)
		return c.Send(adminPanelText, menu, tele.ModeHTML)
	}

	// ==========================================
	// 场景 2: 游客主动退出命令: /guest leave
	// ==========================================
	if len(args) > 1 {
		sub := strings.ToLower(args[1])
		if sub == "leave" || sub == "exit" || sub == "quit" || sub == "off" {
			if h.engine.UnregisterGuest(chatID) || h.engine.UnregisterGuest(senderID) {
				log.Printf("[游客退出] 用户 %d 主动注销游客", senderID)
				return c.Send("👋 您已成功退出游客名单，停止接收全量补货推送。\n<i>(后续随时可再次发送 /register 重新加入体验)</i>", tele.ModeHTML)
			}
			return c.Send("您当前未登记为游客。", tele.ModeHTML)
		}
	}

	// ==========================================
	// 场景 3: 正式白名单会员调用 /guest
	// ==========================================
	if h.engine.IsAuthorized(chatID) || h.engine.IsAuthorized(senderID) {
		return c.Send("💎 <b>您当前已拥有【正式白名单会员】权限</b>\n\n"+
			"• 您已解锁 39 地区自选、极低限价与线路高级正则过滤特权，推送通道享有最高抢占优先级！\n"+
			"• 无需降级为游客体验模式。\n\n"+
			"<i>💡 发送 <code>/menu</code> 打开您的专属定制面板，发送 <code>/sub</code> 可随时启闭推送。</i>", tele.ModeHTML)
	}

	// ==========================================
	// 场景 4: 已经是游客的用户调用 /guest (展示我的游客状态)
	// ==========================================
	if h.engine.IsGuest(chatID) || h.engine.IsGuest(senderID) {
		cfg := h.engine.GetChatConfig(chatID)
		statusStr := "🔔 正常推送中"
		if !cfg.Subscribed {
			statusStr = "🔕 已暂停推送"
		}

		return c.Send(fmt.Sprintf("👤 <b>【Narwhal 游客模式当前状态】</b>\n\n"+
			"• <b>身份权限:</b> 👤 游客体验模式\n"+
			"• <b>您的 ID:</b> <code>%d</code>\n"+
			"• <b>推送通道:</b> 🟢 平台全部未过滤补货广播\n"+
			"• <b>推送状态:</b> %s\n\n"+
			"<b>🛠️ 常用指令速查:</b>\n"+
			"• 发送 <code>/sub</code> - 快速切换开启/暂停推送\n"+
			"• 发送 <code>/mute 1h</code> - 开启临时免打扰 1 小时\n"+
			"• 发送 <code>/guest leave</code> - 注销并退出游客名单\n\n"+
			"<i>💡 提示: 自选地区/限价/正则过滤仅对【正式白名单会员】开放。如需专属定制，请将您的 ID 发送给管理员申请转正！</i>", chatID, statusStr), tele.ModeHTML)
	}

	// ==========================================
	// 场景 5: 未注册新访客调用 /guest (介绍游客模式并引导注册)
	// ==========================================
	guestAvailable := h.engine.IsGuestModeEnabled()
	statusTip := "🟢 当前开放体验中"
	if !guestAvailable {
		statusTip = "🔴 当前暂未开放注册 (请联系管理员)"
	}

	return c.Send(fmt.Sprintf("👋 <b>【Narwhal Cloud 游客体验模式说明】</b>\n\n"+
		"游客模式是为外部 VPS 玩家提供的免费轻量监控体验通道。\n\n"+
		"• <b>通道特性:</b> 免费接收平台全部未过滤的最新补货与上新推送\n"+
		"• <b>当前状态:</b> %s\n\n"+
		"🚀 <b>如何开通体验？</b>\n"+
		"直接发送 <code>/register</code> 即可一键登记开通，秒级激活补货推送！\n\n"+
		"<i>💡 注: 如需精准过滤特定地区（如香港、日本）或月付≤0.5$特价玩具机，可联系管理员开通正式白名单。</i>", statusTip), tele.ModeHTML)
}

// HandleUser 管理员专属白名单用户管理指令
func (h *Handler) HandleUser(c tele.Context) error {
	if c.Chat().Type != tele.ChatPrivate {
		return c.Send("⚠️ 白名单管理指令包含用户隐私，仅限在与 Bot 的私聊中执行。")
	}

	args := strings.Fields(c.Text())
	if len(args) <= 1 {
		usage := `📖 <b>【用户与权限管理指南】</b> (超级管理员专属)

• <code>/user add &lt;ID&gt; [备注]</code> - 授权新用户或群组并激活订阅
• <code>/user upgrade &lt;ID&gt;</code> - 将游客一键转为正式白名单
• <code>/user downgrade &lt;ID&gt;</code> - 将正式白名单降级为游客
• <code>/user del &lt;ID&gt;</code> - 移除白名单或游客并停用推送
• <code>/user list</code> - 查看当前正式白名单列表
• <code>/user guests</code> - 查看当前所有游客白名单列表
• <code>/guest limit &lt;数量&gt;</code> - 设置游客名额限制数 (0为不限)`
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
					"🎉 <b>管理员已为您开通 Narwhal Cloud 监控正式权限！</b>\n\n"+
						"• 官方交互菜单: 输入 <code>/menu</code> 即可定制您的专属地区与价格\n"+
						"• 高级过滤配置: 输入 <code>/filter</code> 定制线路与关键词高级正则\n"+
						"• 过滤配置帮助: 输入 <code>/help</code> 查看规则编写指引\n\n"+
						"<i>💡 您的专属监控已自动开启，有符合您偏好的补货将在此第一时间推送！</i>", tele.ModeHTML)
			}()
		}

		remarkDesc := ""
		if remark != "" {
			remarkDesc = fmt.Sprintf(" (备注: %s)", html.EscapeString(remark))
		}
		log.Printf("[权限管理] 管理员将用户 %d 加入正式白名单%s", targetID, remarkDesc)
		return c.Send(fmt.Sprintf("✅ <b>已成功将 <code>%d</code>%s 加入正式授权白名单！</b>\n• 推送状态: 已自动激活\n• 专属配置: 用户可私聊自由配置地区与规则", targetID, remarkDesc), tele.ModeHTML)

	case "upgrade":
		if len(args) < 3 {
			return c.Send("❌ 用法错误: <code>/user upgrade &lt;ID&gt;</code>", tele.ModeHTML)
		}
		targetID, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			return c.Send("❌ 用户 ID 必须为合法数字", tele.ModeHTML)
		}
		if h.engine.UpgradeGuestToAuth(targetID) {
			log.Printf("[权限管理] 管理员将游客 %d 转正为正式白名单", targetID)
			if targetID > 0 {
				go func() {
					_, _ = c.Bot().Send(&tele.Chat{ID: targetID},
						"🎉 <b>恭喜！管理员已将您升级为【正式白名单会员】！</b>\n\n"+
							"• 您已解锁专属过滤特权！\n"+
							"• 发送 <code>/menu</code> 可自由定制 30+ 地区与价格上限\n"+
							"• 发送 <code>/filter</code> 可定制线路与关键词高级正则", tele.ModeHTML)
				}()
			}
			return c.Send(fmt.Sprintf("✅ <b>已成功将游客 <code>%d</code> 转正为正式白名单！</b>", targetID), tele.ModeHTML)
		}
		return c.Send(fmt.Sprintf("❌ 未找到 ID 为 <code>%d</code> 的用户。", targetID), tele.ModeHTML)

	case "downgrade":
		if len(args) < 3 {
			return c.Send("❌ 用法错误: <code>/user downgrade &lt;ID&gt;</code>", tele.ModeHTML)
		}
		targetID, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			return c.Send("❌ 用户 ID 必须为合法数字", tele.ModeHTML)
		}
		if targetID == h.adminID {
			return c.Send("⚠️ 不能降级超级管理员！", tele.ModeHTML)
		}
		if targetID < 0 {
			return c.Send("⚠️ 群组仅支持正式白名单授权，不支持降级为游客模式。", tele.ModeHTML)
		}
		if h.engine.DowngradeAuthToGuest(targetID) {
			log.Printf("[权限管理] 管理员将用户 %d 降级为游客模式", targetID)
			return c.Send(fmt.Sprintf("✅ 已将 <code>%d</code> 降级为游客模式（全量接收未过滤通知）。", targetID), tele.ModeHTML)
		}
		return c.Send(fmt.Sprintf("❌ 未找到 ID 为 <code>%d</code> 的用户。", targetID), tele.ModeHTML)

	case "guests", "guest":
		return h.HandleBtnAdminGuests(c)

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
			return c.Send(fmt.Sprintf("✅ 已成功将 <code>%d</code> 移出白名单/游客名单并停用通知推送！", targetID), tele.ModeHTML)
		}
		return c.Send(fmt.Sprintf("❌ 未找到 ID 为 <code>%d</code> 的白名单或游客用户。", targetID), tele.ModeHTML)

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
				priceStr = fmt.Sprintf("≤%g$", cfg.QuickMaxPrice)
			}
			sb.WriteString(fmt.Sprintf("   └ 偏好: %s | %s\n\n", regStr, priceStr))
		}

		return c.Send(sb.String(), tele.ModeHTML)

	default:
		return c.Send("❌ 未知子命令。输入 <code>/user</code> 查看用法。", tele.ModeHTML)
	}
}
