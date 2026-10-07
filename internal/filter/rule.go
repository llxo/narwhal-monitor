package filter

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ItemPayload 统一抽象待过滤的套餐属性
type ItemPayload struct {
	Type         string   // 实体类型: "plan"
	ID           string   // 实体唯一 ID
	Title        string   // 套餐名称
	Description  string   // 套餐与机器描述
	Region       string   // 地区代码 (HK, JP, US, etc.)
	Price        float64  // 月付价格
	CPU          int      // 核心数
	RamMB        int      // 内存 MB
	DiskGB       int      // 磁盘 GB
	Bandwidth    int      // 带宽 Mbps
	MachineName  string   // 机器节点名称
	Tags         []string // 机器标签
	IsRestock    bool     // 是否是库存补货（之前售罄或减少，现在恢复）
	Remaining    int      // 剩余库存
	RawSearchStr string   // 预拼接好的全部文本搜索串，加速正则检索
}

// BuildSearchString 拼接所有文本字段为一个搜索字符串，避免多次调用正则搜索
// 自动注入规格关键词（如 64m 128m 1g 1024m 1c 0.2$ 等），让快捷正则能够秒查硬件与价格
func (p *ItemPayload) BuildSearchString() string {
	if p.RawSearchStr != "" {
		return p.RawSearchStr
	}
	var sb strings.Builder
	sb.WriteString(p.Title)
	sb.WriteString(" ")
	sb.WriteString(p.Description)
	sb.WriteString(" ")
	sb.WriteString(p.Region)
	sb.WriteString(" ")
	sb.WriteString(p.MachineName)
	for _, t := range p.Tags {
		sb.WriteString(" ")
		sb.WriteString(t)
	}

	// 自动追加规格关键词，赋能正则直接匹配规格 (例如 /filter regex 64m 或 128m)
	if p.CPU > 0 {
		sb.WriteString(fmt.Sprintf(" %dc %d核", p.CPU, p.CPU))
	}
	if p.RamMB > 0 {
		sb.WriteString(fmt.Sprintf(" %dm %dmb", p.RamMB, p.RamMB))
		if p.RamMB >= 1024 && p.RamMB%1024 == 0 {
			sb.WriteString(fmt.Sprintf(" %dg %dgb", p.RamMB/1024, p.RamMB/1024))
		}
	}
	if p.DiskGB > 0 {
		sb.WriteString(fmt.Sprintf(" %dg %dgb", p.DiskGB, p.DiskGB))
	}
	if p.Price > 0 {
		sb.WriteString(fmt.Sprintf(" %g$ %.2f$", p.Price, p.Price))
	}

	p.RawSearchStr = sb.String()
	return p.RawSearchStr
}

// Rule 代表一条用户持久化存储的过滤规则
type Rule struct {
	ID           string    `json:"id"`            // 规则唯一标识，如 "r-1"
	ChatID       int64     `json:"chat_id"`       // 所属 Telegram Chat ID
	MaxPrice     float64   `json:"max_price"`     // 价格上限 (<=0 表示不限制)
	MinPrice     float64   `json:"min_price"`     // 价格下限 (<=0 表示不限制)
	Regions      []string  `json:"regions"`       // 允许的地区代码列表 (如 ["HK", "JP"], 空表示不限制)
	MinCPU       int       `json:"min_cpu"`       // 核心数下限
	MaxCPU       int       `json:"max_cpu"`       // 核心数上限 (<=0 表示不限制)
	MinRAM       int       `json:"min_ram"`       // 内存下限 (MB)
	MaxRAM       int       `json:"max_ram"`       // 内存上限 (MB, <=0 表示不限制)
	Regex        string    `json:"regex"`         // 正向正则匹配表达式 (满足才匹配)
	ExcludeRegex string    `json:"exclude_regex"` // 反向排除正则表达式 (满足则排除)
	CreatedAt    time.Time `json:"created_at"`
}

// HasPositiveConditions 判断规则是否包含正向筛选条件（若无，则仅作为纯反向排除/黑名单使用）
func (r *Rule) HasPositiveConditions() bool {
	return r.MaxPrice > 0 || r.MinPrice > 0 || len(r.Regions) > 0 ||
		r.MinCPU > 0 || r.MaxCPU > 0 || r.MinRAM > 0 || r.MaxRAM > 0 ||
		strings.TrimSpace(r.Regex) != ""
}

// IsPureExclude 判断是否为纯反向排除规则（无正向条件，仅有排除正则）
func (r *Rule) IsPureExclude() bool {
	return !r.HasPositiveConditions() && strings.TrimSpace(r.ExcludeRegex) != ""
}

// CompiledRule 是 Rule 的内存已编译版本，复用 pre-compiled 正则以实现超低占用与极速匹配
type CompiledRule struct {
	Rule
	CompiledRegex        *regexp.Regexp `json:"-"`
	CompiledExcludeRegex *regexp.Regexp `json:"-"`
}

// NormalizeRegex 确保正则表达式具备 (?i) 前缀以支持忽略大小写（若已有则保持）
func NormalizeRegex(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return ""
	}
	if strings.HasPrefix(pattern, "(?i)") {
		return pattern
	}
	return "(?i)" + pattern
}

// Compile 校验并预编译正则表达式
func (r *Rule) Compile() (*CompiledRule, error) {
	cr := &CompiledRule{
		Rule: *r,
	}

	// 编译正向正则 (自动补充 (?i) 大小写自适应前缀)
	if strings.TrimSpace(r.Regex) != "" {
		normalized := NormalizeRegex(r.Regex)
		re, err := regexp.Compile(normalized)
		if err != nil {
			return nil, fmt.Errorf("正向正则表达式语法错误: %w", err)
		}
		cr.Regex = normalized
		cr.CompiledRegex = re
	}

	// 编译反向排除正则 (自动补充 (?i) 大小写自适应前缀)
	if strings.TrimSpace(r.ExcludeRegex) != "" {
		normalized := NormalizeRegex(r.ExcludeRegex)
		excludeRe, err := regexp.Compile(normalized)
		if err != nil {
			return nil, fmt.Errorf("排除正则表达式语法错误: %w", err)
		}
		cr.ExcludeRegex = normalized
		cr.CompiledExcludeRegex = excludeRe
	}

	// 规范化地区为大写
	for i := range cr.Regions {
		cr.Regions[i] = strings.ToUpper(strings.TrimSpace(cr.Regions[i]))
	}

	return cr, nil
}

// Match 判定一个项目是否命中此规则
func (cr *CompiledRule) Match(p *ItemPayload) bool {
	// 1. 价格过滤
	if cr.MaxPrice > 0 && p.Price > cr.MaxPrice {
		return false
	}
	if cr.MinPrice > 0 && p.Price < cr.MinPrice {
		return false
	}

	// 3. 地区过滤
	if len(cr.Regions) > 0 && p.Region != "" {
		regionMatched := false
		pRegion := strings.ToUpper(p.Region)
		for _, reg := range cr.Regions {
			if reg == pRegion {
				regionMatched = true
				break
			}
		}
		if !regionMatched {
			return false
		}
	}

	// 4. 硬件配置门槛过滤 (CPU / RAM)
	if cr.MinCPU > 0 && p.CPU > 0 && p.CPU < cr.MinCPU {
		return false
	}
	if cr.MaxCPU > 0 && p.CPU > 0 && p.CPU > cr.MaxCPU {
		return false
	}
	if cr.MinRAM > 0 && p.RamMB > 0 && p.RamMB < cr.MinRAM {
		return false
	}
	if cr.MaxRAM > 0 && p.RamMB > 0 && p.RamMB > cr.MaxRAM {
		return false
	}

	// 5. 反向排除正则匹配（如果命中排除正则，则直接过滤排除）
	searchStr := p.BuildSearchString()
	if cr.CompiledExcludeRegex != nil && cr.CompiledExcludeRegex.MatchString(searchStr) {
		return false
	}

	// 6. 正向正则匹配（如果配置了正向正则，必须命中）
	if cr.CompiledRegex != nil && !cr.CompiledRegex.MatchString(searchStr) {
		return false
	}

	return true
}
