package bot

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"narwhal-monitor/internal/api"
	"narwhal-monitor/internal/filter"
	"narwhal-monitor/internal/monitor"
)

var (
	emailRegex    = regexp.MustCompile(`(?i)[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)
	atPrefixRegex = regexp.MustCompile(`([^\s@])@([a-zA-Z0-9_]{3,32})`)
	atSuffixRegex = regexp.MustCompile(`(@[a-zA-Z0-9_]{3,32})([\p{Han}])`)
	nqRegex       = regexp.MustCompile(`(?i)(?:https?://)?(?:[a-zA-Z0-9.-]+\.)?nodequality\.com/(?:r/)?[a-zA-Z0-9_\-\./\?=&%#]+`)
	tqRegex       = regexp.MustCompile(`(?i)(?:https?://)?[a-zA-Z0-9.-]*tcpquality[a-zA-Z0-9.-]*/(?:r/)?[a-zA-Z0-9_\-\./\?=&%#]+`)
)

func cleanQualityURL(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimRight(u, `.,;:!?()[]{}<>、，。；！？）】"'`)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = "https://" + u
	}
	return u
}

// extractQualityLinks 从母机和套餐简介中提取 NodeQuality (NQ) 和 TcpQuality (TQ) 测速链接
func extractQualityLinks(plans ...api.PublicPlan) (string, string) {
	var nqURL, tqURL string
	for _, p := range plans {
		descriptions := []string{p.MachineDescription, p.Description}
		for _, desc := range descriptions {
			if desc == "" {
				continue
			}
			if nqURL == "" {
				if match := nqRegex.FindString(desc); match != "" {
					nqURL = cleanQualityURL(match)
				}
			}
			if tqURL == "" {
				if match := tqRegex.FindString(desc); match != "" {
					tqURL = cleanQualityURL(match)
				}
			}
			if nqURL != "" && tqURL != "" {
				return nqURL, tqURL
			}
		}
	}
	return nqURL, tqURL
}

// formatDescription 优化简介文本排版：自动为未加空格的 @username 前后补充空格，确保 Telegram 客户端正确高亮为可点击的用户名
func formatDescription(s string) string {
	if !strings.Contains(s, "@") {
		return s
	}

	emailMatches := emailRegex.FindAllStringIndex(s, -1)
	isInsideEmail := func(pos int) bool {
		for _, m := range emailMatches {
			if pos >= m[0] && pos < m[1] {
				return true
			}
		}
		return false
	}

	indices := atPrefixRegex.FindAllStringSubmatchIndex(s, -1)
	if len(indices) > 0 {
		var sb strings.Builder
		lastIdx := 0
		for _, loc := range indices {
			atPos := loc[0]
			if isInsideEmail(atPos) {
				sb.WriteString(s[lastIdx:loc[1]])
				lastIdx = loc[1]
				continue
			}
			sb.WriteString(s[lastIdx:loc[3]]) // 保留前导非空白字符
			sb.WriteString(" @")
			sb.WriteString(s[loc[4]:loc[5]]) // username
			lastIdx = loc[1]
		}
		sb.WriteString(s[lastIdx:])
		s = sb.String()
	}

	s = atSuffixRegex.ReplaceAllString(s, "$1 $2")
	return s
}

func formatRAM(mb int) string {
	if mb >= 1024 && mb%1024 == 0 {
		return fmt.Sprintf("%dG", mb/1024)
	}
	return fmt.Sprintf("%dM", mb)
}

func formatTraffic(gb int) string {
	if gb <= 0 {
		return "不限流量"
	}
	if gb >= 1000 && gb%1000 == 0 {
		return fmt.Sprintf("%dTB/月", gb/1000)
	}
	return fmt.Sprintf("%dG/月", gb)
}

func formatPrice(price float64) string {
	s := strconv.FormatFloat(price, 'f', -1, 64)
	return s + "$/月"
}

// RenderSettingsText 渲染内联菜单控制面板的文本摘要
func RenderSettingsText(cfg filter.ChatConfig) string {
	status := "🟢 监控推送中"
	if !cfg.Subscribed {
		status = "🔴 监控已暂停 (/sub on 开启)"
	} else if !cfg.MutedUntil.IsZero() && time.Now().Before(cfg.MutedUntil) {
		status = fmt.Sprintf("🟡 免打扰中 (至 %s 结束)", cfg.MutedUntil.Format("15:04:05"))
	}

	regDesc := "全部地区 (未限制)"
	if len(cfg.QuickRegions) > 0 {
		var parts []string
		for i, r := range cfg.QuickRegions {
			if i >= 6 {
				parts = append(parts, fmt.Sprintf("等共 %d 个地区", len(cfg.QuickRegions)))
				break
			}
			flag := GetRegionFlag(r)
			name := GetRegionName(r)
			parts = append(parts, fmt.Sprintf("%s%s", flag, name))
		}
		regDesc = strings.Join(parts, ", ")
	}

	priceDesc := "不限价格"
	if cfg.QuickMaxPrice > 0 {
		s := strconv.FormatFloat(cfg.QuickMaxPrice, 'f', -1, 64)
		priceDesc = fmt.Sprintf("≤ %s$/月", s)
	}

	var sb strings.Builder
	if cfg.ChatID < 0 {
		groupRemark := ""
		if cfg.Remark != "" {
			groupRemark = fmt.Sprintf(" (%s)", html.EscapeString(cfg.Remark))
		}
		sb.WriteString(fmt.Sprintf("👥 <b>【群组监控控制台%s】</b>\n\n", groupRemark))
		sb.WriteString(fmt.Sprintf("• <b>群组 Chat ID:</b> <code>%d</code>\n", cfg.ChatID))
	} else {
		sb.WriteString("⚙️ <b>【Narwhal Cloud 监控与过滤控制台】</b>\n\n")
	}
	sb.WriteString(fmt.Sprintf("• <b>当前状态:</b> %s\n", status))
	sb.WriteString(fmt.Sprintf("• <b>地区白名单:</b> <code>%s</code>\n", regDesc))
	sb.WriteString(fmt.Sprintf("• <b>价格上限:</b> <code>%s</code>\n", priceDesc))
	sb.WriteString(fmt.Sprintf("• <b>高级自定义规则:</b> %d 条生效中 (/filter list 查看)\n\n", len(cfg.Rules)))
	sb.WriteString("<i>💡 提示: 按钮即点即生效；面板 1 分钟未操作自动清屏自毁，亦可随时点击【🗑️ 关闭面板】立即清理</i>")

	return sb.String()
}

// RenderPlanCard 渲染官方套餐补货/新上架通知卡片 (按宿主机完全聚合)
func RenderPlanCard(evt monitor.Event) (string, *tele.ReplyMarkup) {
	plan := evt.Plan
	if plan == nil && len(evt.TriggeredPlans) > 0 {
		plan = &evt.TriggeredPlans[0]
	}
	if plan == nil {
		return "", nil
	}

	trigPlans := evt.TriggeredPlans
	if len(trigPlans) == 0 {
		trigPlans = []api.PublicPlan{*plan}
	}

	regCode := strings.ToUpper(plan.MachineRegion)
	regFlag := GetRegionFlag(regCode)
	regName := GetRegionName(regCode)

	// 汇总所有触发套餐的名称
	var trigNames []string
	for _, tp := range trigPlans {
		trigNames = append(trigNames, tp.Name)
	}

	stateTag := "上新/补货"
	triggerTag := "刚刚补货"
	allSoldOut := false
	if evt.Type == monitor.EventPlanNew {
		stateTag = "全新上架"
		triggerTag = "全新上架"
	} else if evt.Type == monitor.EventPlanUpdate {
		allSoldOut = true
		for _, tp := range trigPlans {
			if !tp.SoldOut && !tp.RamInsufficient && (tp.Remaining > 0 || tp.Remaining == -1) {
				allSoldOut = false
				break
			}
		}
		if allSoldOut {
			stateTag = "已售罄"
			triggerTag = "已售罄"
		} else {
			stateTag = "库存变动"
			triggerTag = "热销中"
		}
	}

	machineTitle := plan.MachineName
	if machineTitle == "" {
		machineTitle = "Narwhal Cloud 节点"
	}

	var allPlans []api.PublicPlan
	if plan != nil {
		allPlans = append(allPlans, *plan)
	}
	allPlans = append(allPlans, trigPlans...)
	allPlans = append(allPlans, evt.OtherPlans...)
	nqURL, tqURL := extractQualityLinks(allPlans...)

	var qualityLinks []string
	if nqURL != "" {
		qualityLinks = append(qualityLinks, fmt.Sprintf(`<a href="%s">NQ</a>`, html.EscapeString(nqURL)))
	}
	if tqURL != "" {
		qualityLinks = append(qualityLinks, fmt.Sprintf(`<a href="%s">TQ</a>`, html.EscapeString(tqURL)))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🐳 <b>%s</b>\n", html.EscapeString(machineTitle)))
	sb.WriteString(fmt.Sprintf("🌍 <b>地区：</b>%s %s\n", regFlag, regName))
	sb.WriteString(fmt.Sprintf("📢 <b>状态：</b>【%s: %s】\n", stateTag, html.EscapeString(strings.Join(trigNames, ", "))))
	if len(qualityLinks) > 0 {
		sb.WriteString(fmt.Sprintf("📊 <b>NQ/TQ:</b> %s\n", strings.Join(qualityLinks, " ")))
	}
	sb.WriteString("━━━━━━━━━━━━━━\n")

	// 1. 触发套餐列表 (售罄标记为⚪，有货标记为🔥)
	for _, tp := range trigPlans {
		isSold := tp.SoldOut || tp.RamInsufficient || tp.Remaining == 0
		icon := "🔥"
		planTag := triggerTag
		stockStr := "抢购中"
		if isSold {
			icon = "⚪"
			planTag = "已售罄"
			stockStr = "已售罄"
		} else if tp.Remaining > 0 {
			stockStr = fmt.Sprintf("余 %d 台", tp.Remaining)
		} else if !tp.SoldOut && !tp.RamInsufficient {
			stockStr = "充足"
		}
		deployURL := fmt.Sprintf("https://dash.fuckip.me/deploy?plan_id=%s", tp.ID)

		sb.WriteString(fmt.Sprintf("%s <b>%s</b>  [%s] [%s]\n", icon, html.EscapeString(tp.Name), stockStr, planTag))
		sb.WriteString(fmt.Sprintf(" ├ 配置：%d核/%s/%dG/%dMbps\n", tp.CPU, formatRAM(tp.RamMB), tp.DiskGB, tp.BandwidthMbps))
		sb.WriteString(fmt.Sprintf(" ├ 流量：%s | %s\n", formatTraffic(tp.MonthlyTrafficGB), formatPrice(tp.PriceMonthly)))
		sb.WriteString(fmt.Sprintf(" └  👉 <a href=\"%s\">立即下单</a>\n", html.EscapeString(deployURL)))
	}

	// 2. 同一宿主机下的其他可选套餐 (可折叠引用)
	if len(evt.OtherPlans) > 0 {
		var otherSb strings.Builder
		availableCount := 0
		for _, other := range evt.OtherPlans {
			if !other.SoldOut && !other.RamInsufficient {
				availableCount++
			}
		}

		bracketStr := "已售罄"
		if availableCount > 0 {
			bracketStr = fmt.Sprintf("%d 款", availableCount)
		}
		otherSb.WriteString(fmt.Sprintf("📦 <b>其他可选套餐 (%s):</b>\n", bracketStr))
		maxOther := 5
		for i, other := range evt.OtherPlans {
			if i >= maxOther {
				otherSb.WriteString(fmt.Sprintf("<i>... 该机器另有 %d 个套餐未列出</i>\n", len(evt.OtherPlans)-maxOther))
				break
			}
			icon := "✅"
			otherStock := "充足"
			if other.SoldOut || other.RamInsufficient {
				icon = "❌"
				otherStock = "已售罄"
			} else if other.Remaining > 0 {
				otherStock = fmt.Sprintf("余 %d 台", other.Remaining)
			}
			otherURL := fmt.Sprintf("https://dash.fuckip.me/deploy?plan_id=%s", other.ID)

			otherSb.WriteString(fmt.Sprintf("%s <b>%s</b>  [%s]\n", icon, html.EscapeString(other.Name), otherStock))
			otherSb.WriteString(fmt.Sprintf(" ├ 配置：%d核/%s/%dG/%dMbps\n", other.CPU, formatRAM(other.RamMB), other.DiskGB, other.BandwidthMbps))
			otherSb.WriteString(fmt.Sprintf(" ├ 流量：%s | %s\n", formatTraffic(other.MonthlyTrafficGB), formatPrice(other.PriceMonthly)))
			otherSb.WriteString(fmt.Sprintf(" └  👉 <a href=\"%s\">立即下单</a>\n", html.EscapeString(otherURL)))
		}

		sb.WriteString("━━━━━━━━━━━━━━\n")
		sb.WriteString(fmt.Sprintf("<blockquote expandable>%s</blockquote>\n", strings.TrimRight(otherSb.String(), "\n")))
	}

	// 3. 机房/套餐简介（自动折叠）
	var descParts []string
	if md := strings.TrimSpace(plan.MachineDescription); md != "" {
		descParts = append(descParts, html.EscapeString(formatDescription(md)))
	}
	if d := strings.TrimSpace(plan.Description); d != "" && d != strings.TrimSpace(plan.MachineDescription) {
		descParts = append(descParts, html.EscapeString(formatDescription(d)))
	}

	if len(descParts) > 0 {
		sb.WriteString("━━━━━━━━━━━━━━\n")
		sb.WriteString(fmt.Sprintf("<blockquote expandable>💡 <b>简介：</b>\n%s</blockquote>\n", strings.Join(descParts, "\n")))
	}

	sb.WriteString("━━━━━━━━━━━━━━\n")
	if evt.Type == monitor.EventPlanUpdate {
		sb.WriteString(fmt.Sprintf("⏰ <i>检测时间: %s (动态更新)</i>", evt.Timestamp.Format("2006-01-02 15:04:05")))
	} else {
		sb.WriteString(fmt.Sprintf("⏰ <i>检测时间: %s</i>", evt.Timestamp.Format("2006-01-02 15:04:05")))
	}

	// 直达购买按钮 (优先指向有库存套餐，全部售罄时引导至控制台)
	markup := &tele.ReplyMarkup{}
	if allSoldOut {
		btnBuy := markup.URL("⚪ 当前已售罄 (前往控制台)", "https://dash.fuckip.me")
		markup.Inline(markup.Row(btnBuy))
	} else {
		targetPlanID := trigPlans[0].ID
		for _, tp := range trigPlans {
			if !tp.SoldOut && !tp.RamInsufficient && (tp.Remaining > 0 || tp.Remaining == -1) {
				targetPlanID = tp.ID
				break
			}
		}
		firstDeployURL := fmt.Sprintf("https://dash.fuckip.me/deploy?plan_id=%s", targetPlanID)
		btnBuy := markup.URL("🛒 立即下单", firstDeployURL)
		markup.Inline(markup.Row(btnBuy))
	}

	return sb.String(), markup
}
