// Package bot - 平台监控、库存扫描与运维管理
//
// 负责库存实时检索、平台运行状态透视与管理员广播运维：
// • /check     - 立即扫描平台实时在售库存与命中套餐 (快照瞬时检索)
// • /status    - 查看系统监控、物理内存与 Narwhal 账户运行状态
// • /broadcast - 向全体活跃订阅会话群发系统维护广播
//
// 【管理按钮】:
// • btn_admin_menu: 打开管理员主面板
// • btn_admin_toggle_guest: 一键开关游客模式
// • btn_admin_guests: 查看全部游客列表
// • btn_admin_status: 刷新运行状态
// • btn_admin_user_list: 查看正式白名单列表
package bot

import (
	"context"
	"fmt"
	"html"
	"log"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"narwhal-monitor/internal/api"
	"narwhal-monitor/internal/filter"
)

// HandleCheck /check 立即扫描并返回当前符合条件的在售套餐（管理员专属）
func (h *Handler) HandleCheck(c tele.Context) error {
	chatID := c.Chat().ID
	senderID := c.Sender().ID

	log.Printf("[指令执行] 管理员 %d 执行 /check 扫描实时在售套餐", senderID)

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

	// 统计地区库存与符合条件的套餐
	regionStock := make(map[string]int)
	var matchedPlans []api.PublicPlan

	for _, p := range plans {
		if p.SoldOut || p.RamInsufficient || p.Remaining == 0 {
			continue
		}
		reg := strings.ToUpper(p.MachineRegion)
		regionStock[reg]++

		// 构建虚拟 Payload 送入过滤引擎检验
		fullDesc := p.MachineDescription
		if p.Description != "" {
			if fullDesc != "" {
				fullDesc += " " + p.Description
			} else {
				fullDesc = p.Description
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
			sb.WriteString(fmt.Sprintf("  • %s <b>%s</b> | %s\n    规格: %dc/%dM/%dG | 库存: <code>%s</code>\n",
				regFlag, html.EscapeString(p.Name), formatPrice(p.PriceMonthly),
				p.CPU, p.RamMB, p.DiskGB, stockStr))
		}
	}

	sb.WriteString(fmt.Sprintf("\n💡 <i>输入 /menu 可调整过滤规则，或使用 /filter regex 过滤关键词</i>\n"))
	sb.WriteString(fmt.Sprintf("⏰ <i>快照时间: %s (内存瞬时检索)</i>", cacheTime.Format("15:04:05")))

	return c.Send(sb.String(), tele.ModeHTML)
}

// HandleStatus /status 监控与服务健康度概览 (超级管理员专属)
func (h *Handler) HandleStatus(c tele.Context) error {
	if c.Callback() != nil {
		_ = c.Respond()
	}
	if c.Chat().Type != tele.ChatPrivate {
		return c.Send("⚠️ 系统状态包含账户凭据与余额信息，仅限在与 Bot 的私聊中执行。")
	}

	uptime := time.Since(h.startTime).Truncate(time.Second)
	auths := h.engine.GetAllAuthorizedChats()
	subs := h.engine.GetAllSubscribedChats()
	guests := h.engine.GetAllGuestChats()

	var userCount, groupCount int
	for _, cfg := range auths {
		if cfg.ChatID < 0 {
			groupCount++
		} else {
			userCount++
		}
	}

	authStatus := "访客公开模式 (未配置 API_KEY)"
	if h.apiClient != nil && h.apiClient.HasAuth() {
		authStatus = "🟢 已认证 (Authorization: Bearer)"
	}

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	allocMB := float64(m.Alloc) / 1024 / 1024
	sysMB := float64(m.Sys) / 1024 / 1024

	// 智能获取 Linux 进程真实的物理驻留内存 (RSS)，非 Linux 环境回退到 Go Runtime 指标
	memDesc := fmt.Sprintf("堆分配 %.2f MB | 系统保留 %.2f MB", allocMB, sysMB)
	if data, err := os.ReadFile("/proc/self/statm"); err == nil {
		var total, resident int64
		if n, _ := fmt.Sscanf(string(data), "%d %d", &total, &resident); n >= 2 {
			rssMB := float64(resident*int64(os.Getpagesize())) / 1024 / 1024
			memDesc = fmt.Sprintf("物理驻留 %.1f MB (堆分配 %.2f MB)", rssMB, allocMB)
		}
	}

	var sb strings.Builder
	sb.WriteString("📊 <b>【Narwhal Monitor 服务运行状态】</b>\n\n")
	sb.WriteString(fmt.Sprintf("• <b>服务运行时长:</b> %s (轮询中，周期 15s)\n", uptime))
	sb.WriteString(fmt.Sprintf("• <b>活跃推送订阅:</b> <b>%d</b> 个会话 (白名单: %d | 游客: %d)\n", len(subs), userCount+groupCount, len(guests)))
	sb.WriteString(fmt.Sprintf("• <b>授权白名单数:</b> 共 %d 个 (个人: %d | 群组: %d)\n", len(auths), userCount, groupCount))
	sb.WriteString(fmt.Sprintf("• <b>API 鉴权状态:</b> %s\n", authStatus))
	sb.WriteString(fmt.Sprintf("• <b>实时内存占用:</b> %s\n", memDesc))
	sb.WriteString(fmt.Sprintf("• <b>活跃 Go 协程:</b> %d 个\n", runtime.NumGoroutine()))

	if h.apiClient != nil && h.apiClient.HasAuth() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if me, err := h.apiClient.GetMe(ctx); err == nil {
			sb.WriteString(fmt.Sprintf("\n🐳 <b>Narwhal 账户:</b> %s\n", me.Email))
			sb.WriteString(fmt.Sprintf("💰 <b>账户可用余额:</b> $%.2f | 角色: %s\n", me.AvailableBalance, me.Role))
		}
	}

	sb.WriteString("\n<i>💡 输入 <code>/admin</code> 可打开管理控制面板</i>")
	return c.Send(sb.String(), tele.ModeHTML)
}

// HandleBroadcast /broadcast <消息内容> 向所有白名单授权且订阅的会话群发通知
func (h *Handler) HandleBroadcast(c tele.Context) error {
	if c.Chat().Type != tele.ChatPrivate {
		return c.Send("⚠️ 系统广播指令仅限在与 Bot 的私聊中触发执行。")
	}

	rawText := strings.TrimSpace(c.Text())
	firstSpace := strings.IndexAny(rawText, " \t\n")
	if firstSpace == -1 {
		return c.Send("❌ 用法错误: <code>/broadcast &lt;广播内容&gt;</code>", tele.ModeHTML)
	}

	msgContent := strings.TrimSpace(rawText[firstSpace:])
	if msgContent == "" {
		return c.Send("❌ 广播内容不能为空", tele.ModeHTML)
	}
	subscribers := h.engine.GetAllSubscribedChats()
	if len(subscribers) == 0 {
		return c.Send("⚠️ 当前没有任何活跃订阅的会话。")
	}

	broadcastMsg := fmt.Sprintf("📢 <b>【系统全员广播】</b>\n\n%s\n\n⏰ <i>发布时间: %s</i>",
		html.EscapeString(msgContent),
		time.Now().Format("2006-01-02 15:04:05"))

	successCount := 0
	failCount := 0

	log.Printf("[全员广播] 管理员 %d 触发广播，当前订阅列表(%d人): %v，内容: %s", c.Sender().ID, len(subscribers), subscribers, msgContent)

	for _, chatID := range subscribers {
		sentMsg, err := c.Bot().Send(&tele.Chat{ID: chatID}, broadcastMsg, tele.ModeHTML)
		if err != nil {
			failCount++

			// 检查 Telegram 429 Flood Wait 熔断退避
			if floodWait := parseFloodWait(err); floodWait > 0 {
				log.Printf("[广播熔断] 触发 Telegram 429 Flood Wait，暂停广播 %v", floodWait)
				time.Sleep(floodWait)
			}

			errStr := strings.ToLower(err.Error())
			log.Printf("[广播详情] Chat %d 发送失败: %v", chatID, err)
			if strings.Contains(errStr, "blocked") || strings.Contains(errStr, "deactivated") || strings.Contains(errStr, "chat not found") {
				if h.engine.IsGuest(chatID) {
					h.engine.UnregisterGuest(chatID)
					log.Printf("[广播自愈] 检测到游客 Chat %d 已拉黑或注销，已自动注销并释放名额", chatID)
				} else {
					h.engine.SetSubscribed(chatID, false)
					log.Printf("[广播自愈] 检测到白名单 Chat %d 已拉黑或注销，已自动暂停其推送订阅", chatID)
				}
			}
		} else {
			successCount++
			msgID := 0
			if sentMsg != nil {
				msgID = sentMsg.ID
			}
			log.Printf("[广播详情] Chat %d 发送成功 (消息ID: %d)", chatID, msgID)
		}
		// 频率控制：间隔 40ms 发信，限制全局广播在 25 msg/s 内，彻底杜绝 Telegram 429 Flood Wait
		time.Sleep(40 * time.Millisecond)
	}
	log.Printf("[全员广播结束] 成功: %d, 失败: %d", successCount, failCount)

	return c.Send(fmt.Sprintf("✅ <b>广播发送完成！</b>\n• 成功送达: %d 个会话\n• 发送失败: %d 个会话", successCount, failCount), tele.ModeHTML)
}

// HandleAdmin /admin 超级管理员主控制台
func (h *Handler) HandleAdmin(c tele.Context) error {
	if c.Chat().Type != tele.ChatPrivate {
		return c.Send("⚠️ 管理控制台仅限在与 Bot 的私聊中打开，防止控制按钮在群组中暴露。")
	}

	// 动态激活管理员特权菜单
	go h.registerAdminCommands(c.Bot(), c.Sender().ID)

	auths := h.engine.GetAllAuthorizedChats()
	subs := h.engine.GetAllSubscribedChats()
	guestMode := h.engine.IsGuestModeEnabled()
	guestStatus := "🔴 已关闭"
	if guestMode {
		guestStatus = "🟢 已开启"
	}
	limitStr := "不限"
	if max := h.engine.GetMaxGuests(); max > 0 {
		limitStr = fmt.Sprintf("%d人", max)
	}
	guestCount := h.engine.GetGuestCount()

	text := fmt.Sprintf("👑 <b>【Narwhal Monitor 超级管理员控制台】</b>\n\n"+
		"• <b>正式白名单:</b> 共 <b>%d</b> 个专属会话 (推送中: %d 个)\n"+
		"• <b>游客模式:</b> %s (名额: %s | 当前游客: <b>%d</b> 个)\n"+
		"• <b>超级管理员:</b> <code>%d</code> (您自身)\n\n"+
		"<b>🛠️ 管理指令速查:</b>\n"+
		"• <code>/user add &lt;ID&gt; [备注]</code> - 授权新用户为正式白名单\n"+
		"• <code>/user upgrade &lt;ID&gt;</code> - 将游客一键转为正式白名单\n"+
		"• <code>/user del &lt;ID&gt;</code> - 移除白名单或游客\n"+
		"• <code>/user guests</code> - 查看当前全部游客列表\n"+
		"• <code>/guest limit &lt;数量&gt;</code> - 设置游客限制数 (0为不限)\n"+
		"• <code>/status</code> - 查看系统监控与服务运行状态\n"+
		"• <code>/broadcast &lt;内容&gt;</code> - 向全员群发系统维护广播\n\n"+
		"<i>💡 您可以直接点击下方快捷按钮进行操作：</i>",
		len(auths), len(subs), guestStatus, limitStr, guestCount, h.adminID)

	menu := &tele.ReplyMarkup{}
	btnList := menu.Data("📋 正式白名单", "btn_admin_user_list")
	btnGuests := menu.Data("👥 游客列表", "btn_admin_guests")
	btnToggleGuest := menu.Data("🌐 切换游客模式", "btn_admin_toggle_guest")
	btnStatus := menu.Data("📊 运行状态", "btn_admin_status")
	menu.Inline(
		menu.Row(btnList, btnGuests),
		menu.Row(btnToggleGuest, btnStatus),
	)

	return c.Send(text, menu, tele.ModeHTML)
}

// HandleBtnToggleGuest 控制台切换游客模式回调
func (h *Handler) HandleBtnToggleGuest(c tele.Context) error {
	newStatus := !h.engine.IsGuestModeEnabled()
	h.engine.SetGuestModeEnabled(newStatus)
	log.Printf("[系统配置] 管理员通过控制台切换游客模式为: %v", newStatus)
	msg := "已关闭游客模式"
	if newStatus {
		msg = "已开启游客模式"
	}
	_ = c.Respond(&tele.CallbackResponse{Text: msg})

	// 判断当前回调触发自哪个面板并自适应刷新对应页面
	origText := ""
	if c.Message() != nil {
		origText = c.Message().Text
	}

	guestMode := h.engine.IsGuestModeEnabled()
	guestStatus := "🔴 已关闭"
	if guestMode {
		guestStatus = "🟢 已开启"
	}
	maxGuests := h.engine.GetMaxGuests()
	limitStr := "不限名额"
	if maxGuests > 0 {
		limitStr = fmt.Sprintf("%d 人", maxGuests)
	}
	guestCount := h.engine.GetGuestCount()

	if strings.Contains(origText, "游客模式管理中心") {
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
		return c.Edit(adminPanelText, menu, tele.ModeHTML)
	}

	auths := h.engine.GetAllAuthorizedChats()
	subs := h.engine.GetAllSubscribedChats()
	limitAdminStr := "不限"
	if maxGuests > 0 {
		limitAdminStr = fmt.Sprintf("%d人", maxGuests)
	}

	text := fmt.Sprintf("👑 <b>【Narwhal Monitor 超级管理员控制台】</b>\n\n"+
		"• <b>正式白名单:</b> 共 <b>%d</b> 个专属会话 (推送中: %d 个)\n"+
		"• <b>游客模式:</b> %s (名额: %s | 当前游客: <b>%d</b> 个)\n"+
		"• <b>超级管理员:</b> <code>%d</code> (您自身)\n\n"+
		"<b>🛠️ 管理指令速查:</b>\n"+
		"• <code>/user add &lt;ID&gt; [备注]</code> - 授权新用户为正式白名单\n"+
		"• <code>/user upgrade &lt;ID&gt;</code> - 将游客一键转为正式白名单\n"+
		"• <code>/user del &lt;ID&gt;</code> - 移除白名单或游客\n"+
		"• <code>/user guests</code> - 查看当前全部游客列表\n"+
		"• <code>/guest limit &lt;数量&gt;</code> - 设置游客限制数 (0为不限)\n"+
		"• <code>/status</code> - 查看系统监控与服务运行状态\n"+
		"• <code>/broadcast &lt;内容&gt;</code> - 向全员群发系统维护广播\n\n"+
		"<i>💡 您可以直接点击下方快捷按钮进行操作：</i>",
		len(auths), len(subs), guestStatus, limitAdminStr, guestCount, h.adminID)

	menu := &tele.ReplyMarkup{}
	btnList := menu.Data("📋 正式白名单", "btn_admin_user_list")
	btnGuests := menu.Data("👥 游客列表", "btn_admin_guests")
	btnToggleGuest := menu.Data("🌐 切换游客模式", "btn_admin_toggle_guest")
	btnStatus := menu.Data("📊 运行状态", "btn_admin_status")
	menu.Inline(
		menu.Row(btnList, btnGuests),
		menu.Row(btnToggleGuest, btnStatus),
	)

	return c.Edit(text, menu, tele.ModeHTML)
}

// HandleBtnAdminGuests 控制台查看游客列表回调
func (h *Handler) HandleBtnAdminGuests(c tele.Context) error {
	if c.Chat().Type != tele.ChatPrivate {
		return c.Respond(&tele.CallbackResponse{Text: "⚠️ 游客列表包含用户隐私，仅限在私聊中查看", ShowAlert: true})
	}
	_ = c.Respond()
	guests := h.engine.GetAllGuestChats()
	if len(guests) == 0 {
		return c.Send("👥 当前游客列表为空。\n（外部用户输入 <code>/register</code> 可加入游客）", tele.ModeHTML)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("👥 <b>【当前游客白名单列表 (共 %d 个)】</b>\n\n", len(guests)))
	for i, cfg := range guests {
		statusStr := "🔔 接收中"
		if !cfg.Subscribed {
			statusStr = "🔕 已暂停"
		}
		remarkStr := ""
		if cfg.Remark != "" {
			remarkStr = fmt.Sprintf(" (@%s)", html.EscapeString(cfg.Remark))
		}
		sb.WriteString(fmt.Sprintf("<b>#%d</b> 👤 <code>%d</code>%s\n", i+1, cfg.ChatID, remarkStr))
		sb.WriteString(fmt.Sprintf("   ├ 身份: 游客 (全量未过滤) | 状态: %s\n", statusStr))
		sb.WriteString(fmt.Sprintf("   └ 转正: <code>/user upgrade %d</code>\n\n", cfg.ChatID))
	}
	return c.Send(sb.String(), tele.ModeHTML)
}
