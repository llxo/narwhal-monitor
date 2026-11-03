// Package bot - 白名单会员专属过滤规则与交互菜单
//
// 负责会员个性化偏好定制、可视化菜单控制台、高级正则编写与规格解析：
// 【核心命令】:
// • /menu   - 打开交互式设置控制台
// • /filter - 高级正则与多条件过滤规则管理 (/filter list|add|regex|exclude|del|clear)
//
// 【内联交互按钮】:
// • btn_reg: 核心 9 大区切换    • btn_rare_*: 30 个冷门大区切换/全开/清空
// • btn_price: 快捷限价档位切换  • btn_sub_toggle: 面板开关推送
// • btn_refresh: 刷新面板       • btn_menu_home: 返回主菜单
//
// 【规格算法】:
// • parseRAM: 内存智能换算 (64m, 128mb, 1g 等)
// • parsePrice: 货币符号智能剥离与浮点解析
package bot

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"narwhal-monitor/internal/filter"
)

// HandleMenu /menu 打开白名单内联控制台菜单
func (h *Handler) HandleMenu(c tele.Context) error {
	chatID := c.Chat().ID
	cfg := h.engine.GetChatConfig(chatID)
	text := RenderSettingsText(cfg)
	markup := BuildMenuKeyboard(c.Bot(), cfg)
	msg, err := c.Bot().Send(c.Chat(), text, markup, tele.ModeHTML)
	if err != nil {
		return err
	}

	// 全局安排 60 秒滑动自毁（原触发指令不清理保持会话记录）
	h.scheduleAutoDelete(c.Bot(), msg, 60*time.Second)
	return nil
}

// HandleBtnCloseMenu 点击【🗑️ 关闭面板】主动销毁控制台
func (h *Handler) HandleBtnCloseMenu(c tele.Context) error {
	_ = c.Respond()
	if c.Message() != nil {
		h.cancelAutoDelete(c.Message())
	}
	return c.Delete()
}

// refreshMenuAutoDelete 在按钮交互后刷新 60 秒滑动自毁倒计时
func (h *Handler) refreshMenuAutoDelete(c tele.Context) {
	if c.Chat() != nil && c.Message() != nil {
		h.scheduleAutoDelete(c.Bot(), c.Message(), 60*time.Second)
	}
}

// HandleFilter /filter 命令路由分发
func (h *Handler) HandleFilter(c tele.Context) error {
	chatID := c.Chat().ID
	args := getCommandArgs(c)

	// 无参数时输出 /filter 独立命令说明
	if len(args) <= 1 {
		return h.sendFilterHelp(c)
	}

	subCmd := strings.ToLower(args[1])
	switch subCmd {
	case "help":
		return h.sendFilterHelp(c)
	case "list":
		return h.listRules(c, chatID)
	case "clear":
		perm := h.checkPermission(c)
		if !perm.Allowed {
			return h.replyAutoDelete(c, "⚠️ 权限不足: "+perm.Reason+"。")
		}
		h.engine.ClearRules(chatID)
		return h.replyAutoDelete(c, "🧹 已清空当前所有高级过滤规则，恢复默认全量推送状态！")
	case "del", "rm", "delete":
		perm := h.checkPermission(c)
		if !perm.Allowed {
			return h.replyAutoDelete(c, "⚠️ 权限不足: "+perm.Reason+"。")
		}
		if len(args) < 3 {
			return h.replyAutoDelete(c, "❌ 请指定规则 ID，示例: <code>/filter del r1001</code>", tele.ModeHTML)
		}
		ruleID := args[2]
		if h.engine.DeleteRule(chatID, ruleID) {
			return h.replyAutoDelete(c, fmt.Sprintf("✅ 规则 <code>%s</code> 已删除并即时失效！", html.EscapeString(ruleID)), tele.ModeHTML)
		}
		return h.replyAutoDelete(c, fmt.Sprintf("❌ 未找到 ID 为 <code>%s</code> 的规则，输入 <code>/filter list</code> 查看有效 ID。", html.EscapeString(ruleID)), tele.ModeHTML)
	case "regex":
		perm := h.checkPermission(c)
		if !perm.Allowed {
			return h.replyAutoDelete(c, "⚠️ 权限不足: "+perm.Reason+"。")
		}
		if len(args) < 3 {
			return h.replyAutoDelete(c, "❌ 请输入正则表达式，示例: <code>/filter regex 优化|cn2</code>", tele.ModeHTML)
		}
		rawRegex := strings.Join(args[2:], " ")
		return h.addSimpleRegexRule(c, chatID, rawRegex, false)
	case "exclude":
		perm := h.checkPermission(c)
		if !perm.Allowed {
			return h.replyAutoDelete(c, "⚠️ 权限不足: "+perm.Reason+"。")
		}
		if len(args) < 3 {
			return h.replyAutoDelete(c, "❌ 请输入排除表达式，示例: <code>/filter exclude 实验|纯v6</code>", tele.ModeHTML)
		}
		rawRegex := strings.Join(args[2:], " ")
		return h.addSimpleRegexRule(c, chatID, rawRegex, true)
	case "add":
		perm := h.checkPermission(c)
		if !perm.Allowed {
			return h.replyAutoDelete(c, "⚠️ 权限不足: "+perm.Reason+"。")
		}
		return h.parseAndAddRule(c, chatID, args[2:])
	default:
		return h.sendFilterHelp(c)
	}
}

func (h *Handler) sendFilterHelp(c tele.Context) error {
	helpText := `🔍 <b>【Narwhal Monitor 高级过滤规则使用指南】</b>

<b>1. 规则管理命令（点击复制）：</b>
• <code>/filter list</code> - 查看当前生效规则
• <code>/filter regex 优化|cn2</code> - 快速添加正向匹配
• <code>/filter exclude 实验|纯v6</code> - 快速添加反向排除
• <code>/filter add price&lt;=0.5 ram&gt;=256m</code> - 添加复合过滤规则
• <code>/filter del r1001</code> - 删除指定规则
• <code>/filter clear</code> - 清空所有自定义规则

<b>2. 快捷正则示例：</b>
• <code>/filter regex 64m|128m</code> (匹配 64M 或 128M 内存)
• <code>/filter regex 优化|cn2|9929|cmi</code> (三网精品线路)
• <code>/filter regex 双栈|v4|端口</code> (带 IPv4 端口映射节点)
• <code>/filter exclude 实验|无v4|纯v6</code> (排除纯 IPv6 或实验节点)

<b>3. 复合规则示例 (/filter add)：</b>
• <code>/filter add price&lt;=0.5 ram&gt;=256m</code> (月付≤0.5$ 且 内存≥256M)
• <code>/filter add ram&lt;=128m price&lt;=0.3</code> (月付≤0.3$ 且 内存≤128M)
• <code>/filter add price&lt;=1 region=HK,JP</code> (月付≤1$ 且 限定港日)

<i>💡 如需通过按钮点选地区或限价，请输入 /menu 打开控制台。</i>`
	return h.replyAutoDelete(c, helpText, tele.ModeHTML)
}

func (h *Handler) listRules(c tele.Context, chatID int64) error {
	cfg := h.engine.GetChatConfig(chatID)
	if len(cfg.Rules) == 0 {
		return h.replyAutoDelete(c, "📋 当前没有配置任何高级过滤规则，系统按照基础菜单设置推送。\n输入 /help 可查看如何添加规则。", tele.ModeHTML)
	}

	var sb strings.Builder
	sb.WriteString("📋 <b>【当前生效的高级过滤规则列表】</b>\n\n")

	for i, r := range cfg.Rules {
		sb.WriteString(fmt.Sprintf("<b>#%d [ID: <code>%s</code>]</b>\n", i+1, r.ID))
		if r.MaxPrice > 0 {
			sb.WriteString(fmt.Sprintf("  • 价格上限: ≤ %g$\n", r.MaxPrice))
		}
		if r.MinPrice > 0 {
			sb.WriteString(fmt.Sprintf("  • 价格下限: ≥ %g$\n", r.MinPrice))
		}
		if len(r.Regions) > 0 {
			sb.WriteString(fmt.Sprintf("  • 限定地区: <code>%s</code>\n", strings.Join(r.Regions, ", ")))
		}
		if r.MinCPU > 0 {
			sb.WriteString(fmt.Sprintf("  • 核心下限: ≥ %d 核\n", r.MinCPU))
		}
		if r.MaxCPU > 0 {
			sb.WriteString(fmt.Sprintf("  • 核心上限: ≤ %d 核\n", r.MaxCPU))
		}
		if r.MinRAM > 0 {
			sb.WriteString(fmt.Sprintf("  • 内存下限: ≥ %s\n", formatRAM(r.MinRAM)))
		}
		if r.MaxRAM > 0 {
			sb.WriteString(fmt.Sprintf("  • 内存上限: ≤ %s\n", formatRAM(r.MaxRAM)))
		}
		if r.Regex != "" {
			sb.WriteString(fmt.Sprintf("  • 正向正则: <code>%s</code>\n", html.EscapeString(r.Regex)))
		}
		if r.ExcludeRegex != "" {
			sb.WriteString(fmt.Sprintf("  • 排除正则: <code>%s</code>\n", html.EscapeString(r.ExcludeRegex)))
		}
		sb.WriteString(fmt.Sprintf("  <i>(删除此规则: <code>/filter del %s</code>)</i>\n\n", r.ID))
	}

	return h.replyAutoDelete(c, sb.String(), tele.ModeHTML)
}

func (h *Handler) addSimpleRegexRule(c tele.Context, chatID int64, rawRegex string, isExclude bool) error {
	rawRegex = filter.NormalizeRegex(rawRegex)
	// 先行验证正则合法性
	if _, err := regexp.Compile(rawRegex); err != nil {
		return h.replyAutoDelete(c, fmt.Sprintf("❌ <b>正则表达式语法错误</b>:\n<code>%s</code>", html.EscapeString(err.Error())), tele.ModeHTML)
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
		return h.replyAutoDelete(c, fmt.Sprintf("❌ 添加失败: %s", html.EscapeString(err.Error())), tele.ModeHTML)
	}

	return h.replyAutoDelete(c, fmt.Sprintf("✅ <b>规则添加成功并实时生效！</b>\n\n• 规则 ID: <code>%s</code>\n• 描述: %s\n• 删除请用: <code>/filter del %s</code>", cr.ID, desc, cr.ID), tele.ModeHTML)
}

// parseRAM 智能解析内存输入，支持如 64, 64m, 128mb, 512, 1g, 2gb 等规格（转换为 MB）
func parseRAM(val string) int {
	val = strings.ToLower(strings.TrimSpace(val))
	if val == "" {
		return 0
	}
	if strings.HasSuffix(val, "gb") {
		numStr := strings.TrimSpace(strings.TrimSuffix(val, "gb"))
		if f, err := strconv.ParseFloat(numStr, 64); err == nil {
			return int(f * 1024)
		}
	} else if strings.HasSuffix(val, "g") {
		numStr := strings.TrimSpace(strings.TrimSuffix(val, "g"))
		if f, err := strconv.ParseFloat(numStr, 64); err == nil {
			return int(f * 1024)
		}
	} else if strings.HasSuffix(val, "mb") {
		numStr := strings.TrimSpace(strings.TrimSuffix(val, "mb"))
		if i, err := strconv.Atoi(numStr); err == nil {
			return i
		}
	} else if strings.HasSuffix(val, "m") {
		numStr := strings.TrimSpace(strings.TrimSuffix(val, "m"))
		if i, err := strconv.Atoi(numStr); err == nil {
			return i
		}
	} else {
		// 纯数字
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			// 在平台（64MB起步），<= 16 通常意指 GB（如 1 代表 1G，0.5 代表 512M）；>= 32 视为 MB
			if f <= 16 {
				return int(f * 1024)
			}
			return int(f)
		}
	}
	return 0
}

// parsePrice 智能解析价格输入，自动剥离 $, 刀, 元 等货币符号
func parsePrice(val string) float64 {
	val = strings.TrimSpace(val)
	val = strings.TrimPrefix(val, "$")
	val = strings.TrimSuffix(val, "$")
	val = strings.TrimSuffix(val, "刀")
	val = strings.TrimSuffix(val, "元")
	val = strings.TrimSpace(val)
	if f, err := strconv.ParseFloat(val, 64); err == nil {
		return f
	}
	return 0
}

func (h *Handler) parseAndAddRule(c tele.Context, chatID int64, tokens []string) error {
	if len(tokens) == 0 {
		return c.Send("❌ 请提供过滤参数，例如: <code>/filter add price&lt;=0.5 ram&gt;=256m region=HK</code>", tele.ModeHTML)
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
				if f := parsePrice(val); f > 0 {
					rule.MaxPrice = f
				}
			} else if key == "ram" || key == "max_ram" {
				if m := parseRAM(val); m > 0 {
					rule.MaxRAM = m
				}
			} else if key == "cpu" || key == "max_cpu" {
				if i, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
					rule.MaxCPU = i
				}
			}
		} else if strings.Contains(token, ">=") {
			parts := strings.SplitN(token, ">=", 2)
			key, val := strings.ToLower(parts[0]), parts[1]
			if key == "price" || key == "min_price" {
				if f := parsePrice(val); f > 0 {
					rule.MinPrice = f
				}
			} else if key == "cpu" || key == "min_cpu" {
				if i, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
					rule.MinCPU = i
				}
			} else if key == "ram" || key == "min_ram" {
				if m := parseRAM(val); m > 0 {
					rule.MinRAM = m
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
				if f := parsePrice(val); f > 0 {
					rule.MaxPrice = f
				}
			case "min_price":
				if f := parsePrice(val); f > 0 {
					rule.MinPrice = f
				}
			case "cpu", "min_cpu":
				if i, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
					rule.MinCPU = i
				}
			case "max_cpu":
				if i, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
					rule.MaxCPU = i
				}
			case "ram", "min_ram":
				if m := parseRAM(val); m > 0 {
					rule.MinRAM = m
				}
			case "max_ram":
				if m := parseRAM(val); m > 0 {
					rule.MaxRAM = m
				}
			case "regex":
				rule.Regex = filter.NormalizeRegex(val)
			case "exclude_regex", "exclude":
				rule.ExcludeRegex = filter.NormalizeRegex(val)
			}
		} else if rule.Regex == "" {
			// 便捷容错：对未带 key= 的纯关键词参数，自动将其作为正向正则处理 (如 /filter add 家宽|isp)
			rule.Regex = filter.NormalizeRegex(token)
		}
	}

	if rule.MaxPrice == 0 && rule.MinPrice == 0 && len(rule.Regions) == 0 &&
		rule.MinCPU == 0 && rule.MaxCPU == 0 && rule.MinRAM == 0 && rule.MaxRAM == 0 &&
		rule.Regex == "" && rule.ExcludeRegex == "" {
		return h.replyAutoDelete(c, "❌ <b>未能识别到有效的过滤条件</b>\n\n示例：\n• 正则匹配: <code>/filter regex 64m|128m</code>\n• 复合条件: <code>/filter add price&lt;=0.5 ram&gt;=256m</code>", tele.ModeHTML)
	}

	cr, err := h.engine.AddRule(chatID, rule)
	if err != nil {
		return h.replyAutoDelete(c, fmt.Sprintf("❌ 添加规则失败: %s", html.EscapeString(err.Error())), tele.ModeHTML)
	}

	return h.replyAutoDelete(c, fmt.Sprintf("✅ <b>复合过滤规则添加成功并实时生效！</b>\n\n• 规则 ID: <code>%s</code>\n• 可通过 <code>/filter list</code> 查看，或 <code>/filter del %s</code> 删除", cr.ID, cr.ID), tele.ModeHTML)
}

// ---- 白名单内联按钮交互回调 ----

func (h *Handler) HandleBtnRegion(c tele.Context) error {
	_ = c.Respond()
	region := c.Data()
	chatID := c.Chat().ID
	_, isBlocked := h.engine.ToggleRegion(chatID, region, AllSupportedRegions())
	if isBlocked {
		return c.Respond(&tele.CallbackResponse{Text: "⚠️ 至少需保留一个有效地区；若需暂停请点击下方暂停按钮", ShowAlert: true})
	}

	h.refreshMenuAutoDelete(c)
	cfg := h.engine.GetChatConfig(chatID)
	text := RenderSettingsText(cfg)
	markup := BuildMenuKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnPrice(c tele.Context) error {
	_ = c.Respond()
	priceStr := c.Data()
	chatID := c.Chat().ID
	price, _ := strconv.ParseFloat(priceStr, 64)
	h.engine.SetQuickMaxPrice(chatID, price)

	h.refreshMenuAutoDelete(c)
	cfg := h.engine.GetChatConfig(chatID)
	text := RenderSettingsText(cfg)
	markup := BuildMenuKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnSubToggle(c tele.Context) error {
	_ = c.Respond()
	chatID := c.Chat().ID
	cfg := h.engine.GetChatConfig(chatID)
	h.engine.SetSubscribed(chatID, !cfg.Subscribed)

	h.refreshMenuAutoDelete(c)
	newCfg := h.engine.GetChatConfig(chatID)
	text := RenderSettingsText(newCfg)
	markup := BuildMenuKeyboard(c.Bot(), newCfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnRefresh(c tele.Context) error {
	_ = c.Respond()
	chatID := c.Chat().ID
	h.refreshMenuAutoDelete(c)
	cfg := h.engine.GetChatConfig(chatID)
	text := RenderSettingsText(cfg)
	markup := BuildMenuKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnRareMenu(c tele.Context) error {
	_ = c.Respond()
	chatID := c.Chat().ID
	h.refreshMenuAutoDelete(c)
	cfg := h.engine.GetChatConfig(chatID)
	text := RenderRareRegionsText(cfg)
	markup := BuildRareRegionsKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnRareToggle(c tele.Context) error {
	_ = c.Respond()
	reg := strings.ToUpper(strings.TrimSpace(c.Data()))
	chatID := c.Chat().ID
	_, isBlocked := h.engine.ToggleRegion(chatID, reg, AllSupportedRegions())
	if isBlocked {
		return c.Respond(&tele.CallbackResponse{Text: "⚠️ 至少需保留一个有效地区；若需暂停请点击下方暂停按钮", ShowAlert: true})
	}

	h.refreshMenuAutoDelete(c)
	cfg := h.engine.GetChatConfig(chatID)
	text := RenderRareRegionsText(cfg)
	markup := BuildRareRegionsKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnRareAll(c tele.Context) error {
	_ = c.Respond()
	chatID := c.Chat().ID
	// 仅开启全部冷门地区，严格保留一级核心区的现状
	h.engine.EnableRareRegions(chatID, RareRegions, AllSupportedRegions())

	h.refreshMenuAutoDelete(c)
	cfg := h.engine.GetChatConfig(chatID)
	text := RenderRareRegionsText(cfg)
	markup := BuildRareRegionsKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnRareClear(c tele.Context) error {
	_ = c.Respond()
	chatID := c.Chat().ID
	// 仅清空全部冷门地区，严格保留一级核心区的现状
	h.engine.DisableRareRegions(chatID, RareRegions, SupportedQuickRegions)

	h.refreshMenuAutoDelete(c)
	newCfg := h.engine.GetChatConfig(chatID)
	text := RenderRareRegionsText(newCfg)
	markup := BuildRareRegionsKeyboard(c.Bot(), newCfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

func (h *Handler) HandleBtnMenuHome(c tele.Context) error {
	_ = c.Respond()
	chatID := c.Chat().ID
	h.refreshMenuAutoDelete(c)
	cfg := h.engine.GetChatConfig(chatID)
	text := RenderSettingsText(cfg)
	markup := BuildMenuKeyboard(c.Bot(), cfg)
	return c.Edit(text, markup, tele.ModeHTML)
}

// parseBandwidthValue 解析用户输入的带宽参数（支持如 50, 50m, 50mbps, 1g, 1000 等，单位换算为 Mbps）
func parseBandwidthValue(s string) (int, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, false
	}
	if s == "0" || s == "clear" || s == "none" || s == "reset" || s == "off" || s == "不限" {
		return 0, true
	}
	if strings.HasSuffix(s, "gbps") {
		numStr := strings.TrimSpace(strings.TrimSuffix(s, "gbps"))
		if f, err := strconv.ParseFloat(numStr, 64); err == nil && f > 0 {
			return int(f * 1000), true
		}
	} else if strings.HasSuffix(s, "g") {
		numStr := strings.TrimSpace(strings.TrimSuffix(s, "g"))
		if f, err := strconv.ParseFloat(numStr, 64); err == nil && f > 0 {
			return int(f * 1000), true
		}
	} else if strings.HasSuffix(s, "mbps") {
		numStr := strings.TrimSpace(strings.TrimSuffix(s, "mbps"))
		if f, err := strconv.ParseFloat(numStr, 64); err == nil && f > 0 {
			return int(f), true
		}
	} else if strings.HasSuffix(s, "m") {
		numStr := strings.TrimSpace(strings.TrimSuffix(s, "m"))
		if f, err := strconv.ParseFloat(numStr, 64); err == nil && f > 0 {
			return int(f), true
		}
	} else {
		// 纯数字
		if f, err := strconv.ParseFloat(s, 64); err == nil && f >= 0 {
			return int(f), true
		}
	}
	return 0, false
}

// HandleBandwidth /bw 命令：设置或查看带宽过滤阈值
func (h *Handler) HandleBandwidth(c tele.Context) error {
	chatID := c.Chat().ID
	args := getCommandArgs(c)

	perm := h.checkPermission(c)
	if !perm.Allowed {
		return h.replyAutoDelete(c, "⚠️ 权限不足: "+perm.Reason+"。")
	}

	cfg := h.engine.GetChatConfig(chatID)

	// 1. 无参数：查看当前带宽过滤状态与使用说明
	if len(args) <= 1 {
		currentDesc := "不限"
		if cfg.MinBandwidth > 0 && cfg.MaxBandwidth > 0 {
			currentDesc = fmt.Sprintf("%d ~ %d Mbps", cfg.MinBandwidth, cfg.MaxBandwidth)
		} else if cfg.MinBandwidth > 0 {
			currentDesc = fmt.Sprintf("≥ %d Mbps", cfg.MinBandwidth)
		} else if cfg.MaxBandwidth > 0 {
			currentDesc = fmt.Sprintf("≤ %d Mbps", cfg.MaxBandwidth)
		}

		text := fmt.Sprintf(`🌐 <b>【带宽过滤设置】</b>

• <b>当前设置:</b> <code>%s</code>

<b>用法:</b>
• <code>/bw 50</code> - 设置下限为 50 Mbps
• <code>/bw 20 1000</code> - 设置范围为 20 ~ 1000 Mbps
• <code>/bw 0</code> - 清除限制`, currentDesc)
		return h.replyAutoDelete(c, text, tele.ModeHTML)
	}

	// 2. 单参数：/bw 50 或 /bw 0 / clear
	if len(args) == 2 {
		valStr := args[1]
		if strings.EqualFold(valStr, "clear") || strings.EqualFold(valStr, "reset") || strings.EqualFold(valStr, "off") {
			h.engine.SetBandwidth(chatID, 0, 0)
			return h.replyAutoDelete(c, "🧹 已清除带宽限制")
		}

		minBW, ok := parseBandwidthValue(valStr)
		if !ok {
			return h.replyAutoDelete(c, "❌ 参数错误，示例：<code>/bw 50</code>、<code>/bw 20 1000</code>、<code>/bw 0</code>", tele.ModeHTML)
		}

		if minBW <= 0 {
			h.engine.SetBandwidth(chatID, 0, 0)
			return h.replyAutoDelete(c, "🧹 已清除带宽限制")
		}

		h.engine.SetBandwidth(chatID, minBW, 0)
		return h.replyAutoDelete(c, fmt.Sprintf("✅ 带宽下限已设置为 <b>≥ %d Mbps</b>", minBW), tele.ModeHTML)
	}

	// 3. 双参数：/bw 20 1000
	minBW, ok1 := parseBandwidthValue(args[1])
	maxBW, ok2 := parseBandwidthValue(args[2])
	if !ok1 || !ok2 {
		return h.replyAutoDelete(c, "❌ 参数错误，示例：<code>/bw 50</code>、<code>/bw 20 1000</code>、<code>/bw 0</code>", tele.ModeHTML)
	}

	if minBW > maxBW {
		minBW, maxBW = maxBW, minBW
	}

	if minBW <= 0 && maxBW <= 0 {
		h.engine.SetBandwidth(chatID, 0, 0)
		return h.replyAutoDelete(c, "🧹 已清除带宽限制")
	}

	h.engine.SetBandwidth(chatID, minBW, maxBW)
	return h.replyAutoDelete(c, fmt.Sprintf("✅ 带宽范围已设置为 <b>%d ~ %d Mbps</b>", minBW, maxBW), tele.ModeHTML)
}
