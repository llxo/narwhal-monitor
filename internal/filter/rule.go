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
	MinRAM       int       `json:"min_ram"`       // 内存下限 (MB)
	Regex        string    `json:"regex"`         // 正向正则匹配表达式 (满足才匹配)
	ExcludeRegex string    `json:"exclude_regex"` // 反向排除正则表达式 (满足则排除)
	CreatedAt    time.Time `json:"created_at"`
}

// CompiledRule 是 Rule 的内存已编译版本，复用 pre-compiled 正则以实现超低占用与极速匹配
type CompiledRule struct {
	Rule
	CompiledRegex        *regexp.Regexp `json:"-"`
	CompiledExcludeRegex *regexp.Regexp `json:"-"`
}

// Compile 校验并预编译正则表达式
func (r *Rule) Compile() (*CompiledRule, error) {
	cr := &CompiledRule{
		Rule: *r,
	}

	// 编译正向正则
	if strings.TrimSpace(r.Regex) != "" {
		re, err := regexp.Compile(r.Regex)
		if err != nil {
			return nil, fmt.Errorf("正向正则表达式语法错误: %w", err)
		}
		cr.CompiledRegex = re
	}

	// 编译反向排除正则
	if strings.TrimSpace(r.ExcludeRegex) != "" {
		excludeRe, err := regexp.Compile(r.ExcludeRegex)
		if err != nil {
			return nil, fmt.Errorf("排除正则表达式语法错误: %w", err)
		}
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
	if cr.MinRAM > 0 && p.RamMB > 0 && p.RamMB < cr.MinRAM {
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
