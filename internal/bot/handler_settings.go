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
	args := strings.Fields(c.Text())

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
			return h.replyAutoDelete(c, "❌ 用法错误: 请输入要删除的规则 ID，例如: <code>/filter del r1001</code>", tele.ModeHTML)
		}
		ruleID := args[2]
		if h.engine.DeleteRule(chatID, ruleID) {
			return h.replyAutoDelete(c, fmt.Sprintf("✅ 规则 <code>%s</code> 已删除并即时失效！", html.EscapeString(ruleID)), tele.ModeHTML)
		}
		return h.replyAutoDelete(c, fmt.Sprintf("❌ 未找到 ID 为 <code>%s</code> 的规则，请使用 <code>/filter list</code> 查看有效 ID。", html.EscapeString(ruleID)), tele.ModeHTML)
	case "regex":
		perm := h.checkPermission(c)
		if !perm.Allowed {
			return h.replyAutoDelete(c, "⚠️ 权限不足: "+perm.Reason+"。")
		}
		if len(args) < 3 {
			return h.replyAutoDelete(c, "❌ 用法错误: <code>/filter regex &lt;表达式&gt;</code>\n例如: <code>/filter regex (?i)cn2|香港|hk</code>", tele.ModeHTML)
		}
		rawRegex := strings.Join(args[2:], " ")
		return h.addSimpleRegexRule(c, chatID, rawRegex, false)
	case "exclude":
		perm := h.checkPermission(c)
		if !perm.Allowed {
			return h.replyAutoDelete(c, "⚠️ 权限不足: "+perm.Reason+"。")
		}
		if len(args) < 3 {
			return h.replyAutoDelete(c, "❌ 用法错误: <code>/filter exclude &lt;表达式&gt;</code>\n例如: <code>/filter exclude nat|ipv6-only</code>", tele.ModeHTML)
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

💡 <i>平台主要提供特色 NAT 小鸡（多在 1$ 内，内存 64MB 起步），已深度支持精细化规格过滤。</i>

<b>1. 规则管理命令：</b>
• <code>/filter list</code> - 查看当前会话已生效的规则列表
• <code>/filter regex &lt;正则&gt;</code> - 快速添加正向匹配（命中才推，自动忽略大小写）
• <code>/filter exclude &lt;正则&gt;</code> - 快速添加反向排除（命中则丢弃）
• <code>/filter add &lt;参数...&gt;</code> - 添加多维度复合过滤规则
• <code>/filter del &lt;规则ID&gt;</code> - 删除指定规则（如 <code>/filter del r1001</code>）
• <code>/filter clear</code> - 清空当前所有自定义高级规则

<b>2. 快捷正则匹配示例：</b>
• <code>/filter regex 64m|128m</code> <i>(快捷蹲守 64M/128M 超低价玩具机)</i>
• <code>/filter regex 优化|cn2|9929|cmi</code> <i>(仅看三网精品线路，大小写自适应)</i>
• <code>/filter regex 双栈|v4|端口</code> <i>(监控带 IPv4 端口映射的 NAT 节点)</i>
• <code>/filter regex 原生|解锁|流媒体|住宅|家宽</code> <i>(监控特色 IP 与家宽节点)</i>
• <code>/filter exclude 实验|无v4|纯v6</code> <i>(反向排除纯 IPv6 或实验节点)</i>

<b>3. 复合规则语法示例 (/filter add)：</b>
• <code>/filter add price&lt;=0.5 ram&gt;=256m</code> (月付≤0.5$ 且 内存≥256M)
• <code>/filter add ram&lt;=128m price&lt;=0.3</code> (月付≤0.3$ 且 内存≤128M 玩具鸡)
• <code>/filter add price&lt;=1 region=HK,JP</code> (月付≤1$ 且 限定港日)
• <code>/filter add price&lt;=0.5 regex=cmi|bgp</code> (价格与线路组合过滤)
• <code>/filter add 优化|cmi</code> (直接传入关键词快速过滤)

<i>💡 如需通过可视化按钮点选地区或限价，请输入 <code>/menu</code> 打开控制台。</i>`
	return h.replyAutoDelete(c, helpText, tele.ModeHTML)
}

func (h *Handler) listRules(c tele.Context, chatID int64) error {
	cfg := h.engine.GetChatConfig(chatID)
	if len(cfg.Rules) == 0 {
		return h.replyAutoDelete(c, "📋 当前没有配置任何高级过滤规则，系统按照基础菜单设置推送。\n输入 <code>/help</code> 可查看如何添加规则。", tele.ModeHTML)
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
		sb.WriteString(fmt.Sprintf("  <i>(删除此规则: /filter del %s)</i>\n\n", r.ID))
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
			// 在小鸡平台（64MB起步），<= 16 通常意指 GB（如 1 代表 1G，0.5 代表 512M）；>= 32 视为 MB
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
		return h.replyAutoDelete(c, "❌ <b>未能识别到有效的过滤条件</b>\n\n用法示例：\n• 快速添加正则匹配：<code>/filter regex 64m|128m</code>\n• 复合条件过滤：<code>/filter add price&lt;=0.5 ram&gt;=256m region=HK</code>", tele.ModeHTML)
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
