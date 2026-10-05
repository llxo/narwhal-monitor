package bot

import (
	"fmt"
	"strings"

	tele "gopkg.in/telebot.v3"

	"narwhal-monitor/internal/filter"
)

// SupportedQuickRegions 核心 9 大区列表 (3x3 排版，澳门 MO 和台湾 TW 替换 AU 与 NL)
var SupportedQuickRegions = []string{
	"HK", "JP", "US",
	"SG", "KR", "TW",
	"MO", "DE", "GB",
}

// RareRegions 冷门地区列表 (前 6 个按当前在售数量 3x2 排列，后续按在售数量降序排列)
var RareRegions = []string{
	// 前 3x2 (6 个)
	"AU", "NL", "IN",
	"TR", "MY", "FR",
	// 后续常见/稀缺冷门
	"CA", "VN", "RO",
	"NG", "GL", "ZA",
	"TH", "SE", "ID",
	"IL", "UA", "AQ",
	"KP", "CU", "BR",
	"AE", "RU", "KZ",
	"AR", "PL", "IT",
	"ES", "NO", "CL",
}

// PricePresets 快捷价格档位
var PricePresets = []float64{0.2, 0.5, 1.0, 3.0}

// RegionNames 常用地区中文名称映射
var RegionNames = map[string]string{
	"HK": "中国香港",
	"JP": "日本",
	"US": "美国",
	"SG": "新加坡",
	"KR": "韩国",
	"TW": "中国台湾",
	"MO": "中国澳门",
	"DE": "德国",
	"GB": "英国",
	"AU": "澳大利亚",
	"NL": "荷兰",
	"IN": "印度",
	"TR": "土耳其",
	"MY": "马来西亚",
	"FR": "法国",
	"CA": "加拿大",
	"VN": "越南",
	"RO": "罗马尼亚",
	"NG": "尼日利亚",
	"GL": "格陵兰",
	"ZA": "南非",
	"TH": "泰国",
	"SE": "瑞典",
	"ID": "印度尼西亚",
	"IL": "以色列",
	"UA": "乌克兰",
	"AQ": "南极洲",
	"KP": "朝鲜",
	"CU": "古巴",
	"BR": "巴西",
	"AE": "阿联酋",
	"RU": "俄罗斯",
	"KZ": "哈萨克斯坦",
	"AR": "阿根廷",
	"PL": "波兰",
	"IT": "意大利",
	"ES": "西班牙",
	"NO": "挪威",
	"CL": "智利",
	"NZ": "新西兰",
	"MN": "蒙古",
	"PH": "菲律宾",
}

// GetRegionFlag 根据两字母国家/地区代码生成国旗 Emoji，若非两字母国家代码则返回 🌐
func GetRegionFlag(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 {
		return "🌐"
	}
	c0 := rune(code[0])
	c1 := rune(code[1])
	if c0 < 'A' || c0 > 'Z' || c1 < 'A' || c1 > 'Z' {
		return "🌐"
	}
	r1 := 0x1F1E6 + (c0 - 'A')
	r2 := 0x1F1E6 + (c1 - 'A')
	return string([]rune{r1, r2})
}

// GetRegionName 获取地区中文名称，若未收录则返回其代码
func GetRegionName(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if name, ok := RegionNames[code]; ok {
		return name
	}
	return code
}

// BuildMenuKeyboard 构建内联可视化主设置菜单 (3x3 核心大区)
func BuildMenuKeyboard(b *tele.Bot, cfg filter.ChatConfig) *tele.ReplyMarkup {
	menu := &tele.ReplyMarkup{}

	// 1. 核心常用地区按钮行 (9 个分 3 行，每行 3 个)
	var regRows []tele.Row
	for i := 0; i < len(SupportedQuickRegions); i += 3 {
		end := i + 3
		if end > len(SupportedQuickRegions) {
			end = len(SupportedQuickRegions)
		}
		var rowBtns []tele.Btn
		for _, reg := range SupportedQuickRegions[i:end] {
			flag := GetRegionFlag(reg)
			selected := isRegionSelected(cfg.QuickRegions, reg)
			status := "❌"
			if selected {
				status = "✅"
			}
			btnText := fmt.Sprintf("%s %s %s", flag, reg, status)
			rowBtns = append(rowBtns, menu.Data(btnText, "btn_reg", reg))
		}
		regRows = append(regRows, menu.Row(rowBtns...))
	}

	// 更多冷门地区大按钮 (二级入口)
	rareSelectedCount := 0
	if len(cfg.QuickRegions) > 0 {
		coreMap := make(map[string]bool)
		for _, r := range SupportedQuickRegions {
			coreMap[r] = true
		}
		for _, r := range cfg.QuickRegions {
			if !coreMap[strings.ToUpper(r)] {
				rareSelectedCount++
			}
		}
	}
	moreBtnText := "🌍 更多冷门地区 (AU/NL/IN/稀缺等) »"
	if rareSelectedCount > 0 {
		moreBtnText = fmt.Sprintf("🌍 更多冷门地区 (已选 %d 个) »", rareSelectedCount)
	}
	btnMore := menu.Data(moreBtnText, "btn_rare_menu")

	// 2. 价格档位行
	var priceBtns []tele.Btn
	for _, p := range PricePresets {
		status := ""
		if cfg.QuickMaxPrice == p {
			status = "✓"
		}
		btnText := fmt.Sprintf("≤$%g%s", p, status)
		priceBtns = append(priceBtns, menu.Data(btnText, "btn_price", fmt.Sprintf("%g", p)))
	}
	unlimitStatus := ""
	if cfg.QuickMaxPrice <= 0 {
		unlimitStatus = "✓"
	}
	priceBtns = append(priceBtns, menu.Data("不限"+unlimitStatus, "btn_price", "0"))

	// 3. 底部功能行
	subText := "🔔 推送中 (点击暂停)"
	if !cfg.Subscribed {
		subText = "🔕 已暂停 (点击开启)"
	}
	actionBtns := []tele.Btn{
		menu.Data(subText, "btn_sub_toggle"),
		menu.Data("🔄 刷新", "btn_refresh"),
	}
	if cfg.IsAdmin {
		actionBtns = append(actionBtns, menu.Data("👑 管理", "btn_admin_menu"))
	}

	// 组装所有行
	var allRows []tele.Row
	allRows = append(allRows, regRows...)
	allRows = append(allRows, menu.Row(btnMore))
	allRows = append(allRows, menu.Row(priceBtns...))
	allRows = append(allRows, menu.Row(actionBtns...))

	menu.Inline(allRows...)
	return menu
}

// BuildRareRegionsKeyboard 构建二级冷门地区扁平菜单 (顶部一键全开/全清，3列排布)
func BuildRareRegionsKeyboard(b *tele.Bot, cfg filter.ChatConfig) *tele.ReplyMarkup {
	menu := &tele.ReplyMarkup{}
	var rows []tele.Row

	// 顶部功能行：一键开启全部 和 一键清空全部
	btnAll := menu.Data("🔄 一键开启全部", "btn_rare_all")
	btnClear := menu.Data("🧹 一键清空全部", "btn_rare_clear")
	rows = append(rows, menu.Row(btnAll, btnClear))

	// 冷门地区列表 (3 列排布)
	for i := 0; i < len(RareRegions); i += 3 {
		end := i + 3
		if end > len(RareRegions) {
			end = len(RareRegions)
		}
		var rowBtns []tele.Btn
		for _, reg := range RareRegions[i:end] {
			flag := GetRegionFlag(reg)
			name := GetRegionName(reg)
			selected := isRegionSelected(cfg.QuickRegions, reg)
			status := "❌"
			if selected {
				status = "✅"
			}
			btnText := fmt.Sprintf("%s %s %s", flag, name, status)
			rowBtns = append(rowBtns, menu.Data(btnText, "btn_rare_toggle", reg))
		}
		rows = append(rows, menu.Row(rowBtns...))
	}

	// 底部返回主控制台按钮
	btnBack := menu.Data("« 🏠 返回主控制台", "btn_menu_home")
	rows = append(rows, menu.Row(btnBack))

	menu.Inline(rows...)
	return menu
}

// RenderRareRegionsText 渲染二级冷门地区菜单的文本摘要
func RenderRareRegionsText(cfg filter.ChatConfig) string {
	totalSelected := len(cfg.QuickRegions)
	selDesc := "全局未限制模式（全球所有节点均接收）"
	if totalSelected > 0 {
		selDesc = fmt.Sprintf("已激活 <b>%d</b> 个白名单地区", totalSelected)
	}

	return fmt.Sprintf("🌍 <b>【更多冷门与特色地区选择】</b>\n\n"+
		"• <b>白名单状态:</b> %s\n\n"+
		"<i>💡 顶部可一键全开/清空，点击国家按钮即时开关；随时可一键返回主菜单。</i>", selDesc)
}

func isRegionSelected(selectedList []string, reg string) bool {
	if len(selectedList) == 0 {
		// 空表示全选
		return true
	}
	reg = strings.ToUpper(reg)
	for _, r := range selectedList {
		if strings.ToUpper(r) == reg {
			return true
		}
	}
	return false
}
