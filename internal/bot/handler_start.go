// Package bot - 门面向导与通用公共指令
//
// 负责系统入口欢迎、语法指南、会话查询与基础推送开关：
// • /start  - 首次欢迎向导（根据管理员/VIP/游客/访客自适应差异化呈现）
// • /help   - 完整命令与正则过滤语法指南
// • /id     - 查询当前会话的 Chat ID
// • /sub    - 一键快速切换通知推送开启/暂停 (无需参数直接 Toggle)
// • /mute   - 开启临时免打扰 (/mute 1h | /mute 0)
package bot

import (
	"fmt"
	"log"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"
)

// HandleStart /start 命令欢迎与向导引导（根据用户身份权限自适应渲染）
func (h *Handler) HandleStart(c tele.Context) error {
	chatID := c.Chat().ID
	senderID := int64(0)
	if c.Sender() != nil {
		senderID = c.Sender().ID
	}

	if c.Chat().Type != tele.ChatPrivate && !h.hasPermission(c) {
		return h.replyAutoDelete(c, "⚠️ 仅管理员或群主有权在群组内执行 /start 初始化设置。")
	}

	// 确保当前会话已激活并加入推送列表（仅对已授权会话生效）
	h.engine.EnsureChatSubscribed(chatID)

	// 1. 👑 超级管理员
	if h.isAdmin(c) {
		go h.registerAdminCommands(c.Bot(), senderID)
		text := "👑 <b>【Narwhal Cloud 超级管理员控制台】</b>\n\n" +
			fmt.Sprintf("• <b>超级管理员 ID:</b> <code>%d</code> (您自身)\n", chatID) +
			"• <b>服务架构:</b> 实时差分监控 + 毫秒级补货广播 + 防洪限流熔断\n\n" +
			"<b>🛠️ 管理员特权指令速查:</b>\n" +
			"• <code>/admin</code> - 打开管理员专属主控制面板\n" +
			"• <code>/check</code> - 立即扫描平台实时在售库存与命中套餐\n" +
			"• <code>/status</code> - 查看系统监控、物理内存与账户运行状态\n" +
			"• <code>/user</code> - 白名单授权、转正与删除管理\n" +
			"• <code>/guest</code> - 游客模式开关与名额配额控制\n" +
			"• <code>/broadcast</code> - 向全体活跃订阅会话群发广播\n\n" +
			"<i>💡 输入 <code>/admin</code> 可调出控制台面板快速点按操作。</i>"
		return c.Send(text, tele.ModeHTML)
	}

	// 2. 💎 正式白名单会员 (VIP，群聊严格仅认群组自身白名单)
	if h.engine.IsAuthorized(chatID) {
		cfg := h.engine.GetChatConfig(chatID)
		statusStr := "🟢 接收中"
		if !cfg.Subscribed {
			statusStr = "🔕 已暂停"
		} else if !cfg.MutedUntil.IsZero() && time.Now().Before(cfg.MutedUntil) {
			statusStr = "🟡 静音中"
		}

		text := "💎 <b>【Narwhal Cloud 专属监控管家】</b>\n\n" +
			"• <b>身份权限:</b> 💎 正式白名单会员\n" +
			fmt.Sprintf("• <b>会话 Chat ID:</b> <code>%d</code>\n", chatID) +
			fmt.Sprintf("• <b>推送状态:</b> %s\n\n", statusStr) +
			"🚀 <b>专属功能与快捷操作:</b>\n" +
			"• 发送 <code>/menu</code> - 打开交互式控制台，自由定制 39 个地区白名单与最高限价\n" +
			"• 发送 <code>/filter</code> - 配置线路、IPv4、内存或关键词高级正则过滤规则\n" +
			"• 发送 <code>/sub</code> - 一键快速切换通知推送 (开启/暂停)\n" +
			"• 发送 <code>/mute 1h</code> - 开启临时免打扰 (支持 30m, 2h 等)\n" +
			"• 发送 <code>/help</code> - 查看完整规则编写指南与实战语法示例\n\n" +
			"<i>💡 您的监控规则专属独立隔离，有符合偏好的新补货将在此第一时间推送！</i>"
		return h.replyAutoDelete(c, text, tele.ModeHTML)
	}

	// 3. 👤 游客体验用户 (Guest，仅限私聊)
	if c.Chat().Type == tele.ChatPrivate && h.engine.IsGuest(chatID) {
		cfg := h.engine.GetChatConfig(chatID)
		statusStr := "🟢 接收中"
		if !cfg.Subscribed {
			statusStr = "🔕 已暂停"
		}

		text := "👋 <b>【Narwhal Cloud 游客监控体验】</b>\n\n" +
			"• <b>身份权限:</b> 👤 游客体验模式\n" +
			fmt.Sprintf("• <b>会话 Chat ID:</b> <code>%d</code>\n", chatID) +
			fmt.Sprintf("• <b>推送状态:</b> %s\n\n", statusStr) +
			"• <b>推送通道:</b> 🟢 默认接收平台<b>全部未过滤</b>的补货与上新广播\n\n" +
			"<b>🛠️ 可用指令速查:</b>\n" +
			"• 发送 <code>/sub</code> - 一键切换通知推送 (开启/暂停)\n" +
			"• 发送 <code>/mute 1h</code> - 开启临时免打扰\n" +
			"• 发送 <code>/guest leave</code> - 注销并退出游客名单\n\n" +
			"<i>💡 提示: 自选地区、最高限价与高级正则过滤仅对【正式白名单会员】开放。如需专属定制规则，请将上方 Chat ID 发送给管理员申请升级！</i>"
		return h.replyAutoDelete(c, text, tele.ModeHTML)
	}

	// 4. ⚪ 访客 / 未授权新用户
	guestAvailable := h.engine.IsGuestModeEnabled() && c.Chat().Type == tele.ChatPrivate
	guestTip := ""
	if guestAvailable {
		guestTip = "🎉 <b>当前系统已开放免费游客体验！</b>\n" +
			"直接发送 <code>/register</code> 即可一键登记为游客，立刻激活接收平台全量最新补货推送！\n\n"
	}

	idType := "您的 Chat ID"
	if c.Chat().Type != tele.ChatPrivate {
		idType = "群组 Chat ID"
	}

	text := "👋 <b>欢迎了解 Narwhal Cloud 实时监控与补货通知 Bot！</b>\n\n" +
		fmt.Sprintf("• <b>%s:</b> <code>%d</code>\n", idType, chatID) +
		"• <b>运行模式:</b> 白名单定制化监控服务\n\n" +
		guestTip +
		"💎 <b>正式白名单会员特权:</b>\n" +
		"• 支持 39 个地区自选白名单过滤 (港/日/美/新/德/英等)\n" +
		"• 支持月付价格上限精准过滤 (如 ≤0.5$ 极低成本小鸡)\n" +
		"• 支持 CN2 / CMI / 9929 / NAT双栈 等高级正则过滤\n" +
		"• 享最高优先级抢占推送通道\n\n" +
		"<i>💡 如需开通正式白名单，请将上方 Chat ID 发送给管理员。</i>"

	return h.replyAutoDelete(c, text, tele.ModeHTML)
}

// HandleHelp /help 使用帮助
func (h *Handler) HandleHelp(c tele.Context) error {
	var sb strings.Builder
	sb.WriteString(`📖 <b>【Narwhal Monitor 命令与正则过滤使用指南】</b>

<b>1. 基础与监控</b>
• <code>/menu</code> - 调出交互式按钮控制台（点按切换地区/限价）
• <code>/id</code> - 查看当前私聊或群聊的 Chat ID

<b>2. 规则快速过滤命令（即设即生效）</b>
• <code>/filter list</code> - 查看当前已生效的规则列表
• <code>/filter add [条件...]</code> - 添加复合过滤规则
• <code>/filter regex &lt;正则表达式&gt;</code> - 快速添加正向正则过滤（满足才推）
• <code>/filter exclude &lt;正则表达式&gt;</code> - 快速添加反向正则排除（满足则丢弃）
• <code>/filter del &lt;规则ID&gt;</code> - 删除指定规则
• <code>/filter clear</code> - 清空所有自定义规则

<b>3. 实用场景与语法示例：</b>
• <code>/filter regex 64m|128m</code> (快捷监控 64M 或 128M 特价玩具小鸡)
• <code>/filter regex 优化|cn2|9929|cmi</code> (仅看三网精品线路)
• <code>/filter regex 双栈|v4|端口</code> (监控带 IPv4 端口映射的 NAT 节点)
• <code>/filter exclude 实验|无v4|纯v6</code> (排除实验或纯 IPv6 节点)
• <code>/filter add price&lt;=0.5 ram&gt;=256m</code> (月付≤0.5$ 且 内存≥256MB)
• <code>/filter add ram&lt;=128m price&lt;=0.3</code> (淘 ≤128M 极低成本玩具机)
• <code>/filter add price&lt;=1 region=HK,JP</code> (月付≤1$ 且限定香港或日本)

<b>4. 通知开关与免打扰</b>
• <code>/sub</code> - 一键快速切换通知推送 (开启/暂停)
• <code>/mute 2h</code> - 临时免打扰 2 小时 (支持 30m, 1h, 6h 等)`)

	if h.isAdmin(c) {
		sb.WriteString("\n\n<b>5. 👑 超级管理员特权指令</b>\n" +
			"• <code>/check</code> - 立即扫描平台当前在售库存与命中套餐\n" +
			"• <code>/status</code> - 查看系统监控运行状态与账户余额\n" +
			"• <code>/user add &lt;ID&gt; [备注]</code> - 授权新用户或群组并激活订阅\n" +
			"• <code>/user del &lt;ID&gt;</code> - 移除白名单或游客并停用推送\n" +
			"• <code>/user list</code> - 查看完整白名单列表与规则状态\n" +
			"• <code>/guest limit &lt;数量&gt;</code> - 设置游客名额限制数 (0为不限)\n" +
			"• <code>/broadcast &lt;内容&gt;</code> - 向全员群发系统维护广播")
		return c.Send(sb.String(), tele.ModeHTML)
	}

	return h.replyAutoDelete(c, sb.String(), tele.ModeHTML)
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

	return h.replyAutoDelete(c, reply, tele.ModeHTML)
}

// HandleSub /sub 命令切换全局推送开关 (无参数时自动 Toggle 开启/暂停)
func (h *Handler) HandleSub(c tele.Context) error {
	args := getCommandArgs(c)
	chatID := c.Chat().ID
	cfg := h.engine.GetChatConfig(chatID)

	// 核心：无参数时自动翻转当前订阅状态 (Toggle)
	newSub := !cfg.Subscribed

	// 若带有显式参数，兼容 /sub on 与 /sub off
	if len(args) > 1 {
		action := strings.ToLower(args[1])
		if action == "on" || action == "true" || action == "enable" || action == "1" {
			newSub = true
		} else if action == "off" || action == "false" || action == "disable" || action == "0" {
			newSub = false
		} else {
			return h.replyAutoDelete(c, "💡 <b>用法提示:</b>\n• 直接发送 <code>/sub</code> 即可快速切换开启/暂停\n• 亦可指定状态: <code>/sub on</code> | <code>/sub off</code>", tele.ModeHTML)
		}
	}

	h.engine.SetSubscribed(chatID, newSub)

	if newSub {
		log.Printf("[订阅切换] 会话 %d 开启推送通知", chatID)
		return h.replyAutoDelete(c, "🔔 <b>通知推送已开启！</b>\n有满足条件的新补货将第一时间推送给您。\n<i>(再次发送 <code>/sub</code> 可随时暂停推送)</i>", tele.ModeHTML)
	}

	log.Printf("[订阅切换] 会话 %d 暂停推送通知", chatID)
	return h.replyAutoDelete(c, "🔕 <b>通知推送已暂停！</b>\n已暂时停止向该会话发送补货通知。\n<i>(再次发送 <code>/sub</code> 即可立即恢复接收)</i>", tele.ModeHTML)
}

// HandleMute /mute 临时免打扰
func (h *Handler) HandleMute(c tele.Context) error {
	args := getCommandArgs(c)
	chatID := c.Chat().ID

	if len(args) <= 1 {
		return h.replyAutoDelete(c, "用法: <code>/mute 1h</code> (免打扰1小时)，或 <code>/mute 0</code> (解除静音)", tele.ModeHTML)
	}

	durationStr := args[1]
	if durationStr == "0" || durationStr == "off" {
		h.engine.SetMute(chatID, 0)
		return h.replyAutoDelete(c, "🔔 已解除静音，恢复正常接收通知！")
	}

	d, err := time.ParseDuration(durationStr)
	if err != nil {
		return h.replyAutoDelete(c, "❌ 时长格式不合法，支持如: <code>30m</code>, <code>1h</code>, <code>2h</code>, <code>24h</code>", tele.ModeHTML)
	}

	until := h.engine.SetMute(chatID, d)
	return h.replyAutoDelete(c, fmt.Sprintf("🔕 已开启临时免打扰，直到 <b>%s</b> 为止（持续 %v）。", until.Format("2006-01-02 15:04:05"), d), tele.ModeHTML)
}
