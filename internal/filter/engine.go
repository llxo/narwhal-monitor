package filter

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ChatConfig 保存单个 Chat 的订阅配置与过滤规则
type ChatConfig struct {
	ChatID        int64           `json:"chat_id"`
	IsAdmin       bool            `json:"is_admin,omitempty"`// 是否是超级管理员会话
	Remark        string          `json:"remark,omitempty"` // 用户或群组备注说明
	Authorized    bool            `json:"authorized"`       // 是否已获得白名单授权
	Subscribed    bool            `json:"subscribed"`       // 是否开启通知
	MutedUntil    time.Time       `json:"muted_until"`      // 静音截至时间
	QuickRegions  []string        `json:"quick_regions"`    // 内联菜单选中的快速地区白名单（空表示全选）
	QuickMaxPrice float64         `json:"quick_max_price"`  // 内联菜单选中的快速限价（<=0 表示不限）
	Rules         []Rule          `json:"rules"`            // 高级过滤规则列表
	compiledRules []*CompiledRule `json:"-"`                // 内存中已预编译规则缓存
}

// Engine 是多用户并发安全的高性能过滤规则引擎
type Engine struct {
	mu           sync.RWMutex
	adminID      int64
	chats        map[int64]*ChatConfig
	onSaveNeeded func(chats map[int64]*ChatConfig) // 持久化回调
	saveChan     chan map[int64]*ChatConfig        // 串行持久化消费管道
	stopChan     chan struct{}                     // 退出信号
}

// NewEngine 创建并初始化过滤引擎
func NewEngine(onSave func(chats map[int64]*ChatConfig)) *Engine {
	e := &Engine{
		chats:        make(map[int64]*ChatConfig),
		onSaveNeeded: onSave,
		saveChan:     make(chan map[int64]*ChatConfig, 1),
		stopChan:     make(chan struct{}),
	}
	if onSave != nil {
		go e.runSaveWorker()
	}
	return e
}

func (e *Engine) runSaveWorker() {
	for {
		select {
		case <-e.stopChan:
			return
		case snapshot, ok := <-e.saveChan:
			if !ok {
				return
			}
		drain:
			for {
				select {
				case latest, ok := <-e.saveChan:
					if !ok {
						if e.onSaveNeeded != nil {
							e.onSaveNeeded(snapshot)
						}
						return
					}
					snapshot = latest
				default:
					break drain
				}
			}
			if e.onSaveNeeded != nil {
				e.onSaveNeeded(snapshot)
			}
		}
	}
}

// Close 安全停止持久化后台协程
func (e *Engine) Close() {
	select {
	case <-e.stopChan:
	default:
		close(e.stopChan)
	}
}

// LoadConfigs 从持久化存储中批量加载配置，并预编译所有正则规则
func (e *Engine) LoadConfigs(configs map[int64]*ChatConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	for chatID, cfg := range configs {
		var compiled []*CompiledRule
		for _, r := range cfg.Rules {
			cr, err := r.Compile()
			if err == nil {
				compiled = append(compiled, cr)
			}
		}
		cfg.compiledRules = compiled
		e.chats[chatID] = cfg
	}
	return nil
}

// GetOrCreateChat 获取或初始化一个 Chat 的配置（并发安全）
func (e *Engine) getOrCreateChatLocked(chatID int64) *ChatConfig {
	cfg, ok := e.chats[chatID]
	if !ok {
		cfg = &ChatConfig{
			ChatID:     chatID,
			Subscribed: true,
			Rules:      make([]Rule, 0),
		}
		e.chats[chatID] = cfg
	}
	return cfg
}

// EnsureChatSubscribed 确保已授权 Chat 处于订阅推送状态（绝不越权自动赋予白名单权限）
func (e *Engine) EnsureChatSubscribed(chatID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg, ok := e.chats[chatID]
	isAuth := (e.adminID != 0 && chatID == e.adminID) || (ok && cfg.Authorized)
	if !isAuth {
		return
	}

	if cfg == nil {
		cfg = e.getOrCreateChatLocked(chatID)
		cfg.Authorized = true
	}
	if !cfg.Subscribed {
		cfg.Subscribed = true
		e.notifySaveLocked()
	}
}

// SetAdminID 设置单一超级管理员，管理员始终拥有永久白名单授权与推送开关
func (e *Engine) SetAdminID(adminID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.adminID = adminID
	if adminID != 0 {
		cfg := e.getOrCreateChatLocked(adminID)
		cfg.Authorized = true
		if cfg.Remark == "" {
			cfg.Remark = "超级管理员"
		}
		cfg.Subscribed = true
	}
}

// GetAdminID 获取当前配置的单一超级管理员 ID
func (e *Engine) GetAdminID() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.adminID
}

// IsAuthorized 判断某个 ID 是否在白名单中（管理员永久授权）
func (e *Engine) IsAuthorized(id int64) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if id != 0 && id == e.adminID {
		return true
	}
	cfg, ok := e.chats[id]
	return ok && cfg.Authorized
}

// AuthorizeChat 将指定 Chat 加入白名单授权并激活订阅
func (e *Engine) AuthorizeChat(chatID int64, remark string) *ChatConfig {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg := e.getOrCreateChatLocked(chatID)
	cfg.Authorized = true
	if remark != "" {
		cfg.Remark = remark
	}
	cfg.Subscribed = true
	e.notifySaveLocked()
	return cfg
}

// RevokeChat 取消指定 Chat 的白名单授权并停止推送
func (e *Engine) RevokeChat(chatID int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if chatID == e.adminID {
		return false // 管理员不可被取消授权
	}
	cfg, ok := e.chats[chatID]
	if !ok || !cfg.Authorized {
		return false
	}
	cfg.Authorized = false
	cfg.Subscribed = false
	e.notifySaveLocked()
	return true
}

// GetAllAuthorizedChats 获取所有处于白名单授权中的 Chat 配置快照
func (e *Engine) GetAllAuthorizedChats() []ChatConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var list []ChatConfig
	for id, cfg := range e.chats {
		isAuth := cfg.Authorized || (e.adminID != 0 && id == e.adminID)
		if isAuth {
			copyCfg := *cfg
			copyCfg.IsAdmin = (e.adminID != 0 && id == e.adminID)
			copyCfg.Rules = append([]Rule(nil), cfg.Rules...)
			copyCfg.QuickRegions = append([]string(nil), cfg.QuickRegions...)
			list = append(list, copyCfg)
		}
	}
	return list
}

// AddRule 动态添加一条过滤规则，实时生效并持久化
func (e *Engine) AddRule(chatID int64, rule Rule) (*CompiledRule, error) {
	cr, err := rule.Compile()
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	cfg := e.getOrCreateChatLocked(chatID)
	if rule.ID == "" {
		existing := make(map[string]bool, len(cfg.Rules))
		for _, r := range cfg.Rules {
			existing[strings.ToLower(r.ID)] = true
		}
		seq := 1001
		for {
			candidate := fmt.Sprintf("r%d", seq)
			if !existing[candidate] {
				rule.ID = candidate
				break
			}
			seq++
		}
	}
	rule.ChatID = chatID
	rule.CreatedAt = time.Now()
	cr.Rule = rule

	cfg.Rules = append(cfg.Rules, rule)
	cfg.compiledRules = append(cfg.compiledRules, cr)

	e.notifySaveLocked()
	return cr, nil
}

// DeleteRule 动态删除指定规则，实时生效
func (e *Engine) DeleteRule(chatID int64, ruleID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg, ok := e.chats[chatID]
	if !ok {
		return false
	}

	found := false
	newRules := make([]Rule, 0, len(cfg.Rules))
	newCompiled := make([]*CompiledRule, 0, len(cfg.compiledRules))

	for i, r := range cfg.Rules {
		if strings.EqualFold(r.ID, ruleID) {
			found = true
			continue
		}
		newRules = append(newRules, r)
		if i < len(cfg.compiledRules) {
			newCompiled = append(newCompiled, cfg.compiledRules[i])
		}
	}

	if found {
		cfg.Rules = newRules
		cfg.compiledRules = newCompiled
		e.notifySaveLocked()
	}
	return found
}

// ClearRules 清空该 Chat 的所有高级过滤规则，实时生效
func (e *Engine) ClearRules(chatID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg, ok := e.chats[chatID]
	if ok {
		cfg.Rules = make([]Rule, 0)
		cfg.compiledRules = make([]*CompiledRule, 0)
		e.notifySaveLocked()
	}
}

// GetChatConfig 获取指定 Chat 的完整配置只读快照
func (e *Engine) GetChatConfig(chatID int64) ChatConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()

	cfg, ok := e.chats[chatID]
	if !ok {
		return ChatConfig{
			ChatID:     chatID,
			IsAdmin:    e.adminID != 0 && chatID == e.adminID,
			Subscribed: true,
		}
	}

	// 浅拷贝返回，保证并发安全
	copyCfg := *cfg
	copyCfg.IsAdmin = (e.adminID != 0 && chatID == e.adminID)
	copyCfg.Rules = append([]Rule(nil), cfg.Rules...)
	copyCfg.QuickRegions = append([]string(nil), cfg.QuickRegions...)
	return copyCfg
}

// SetSubscribed 设置订阅开关，实时生效
func (e *Engine) SetSubscribed(chatID int64, sub bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg := e.getOrCreateChatLocked(chatID)
	cfg.Subscribed = sub
	e.notifySaveLocked()
}

// SetMute 设置临时免打扰时长，实时生效
func (e *Engine) SetMute(chatID int64, d time.Duration) time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg := e.getOrCreateChatLocked(chatID)
	if d <= 0 {
		cfg.MutedUntil = time.Time{}
	} else {
		cfg.MutedUntil = time.Now().Add(d)
	}
	e.notifySaveLocked()
	return cfg.MutedUntil
}

// ToggleRegion 切换快速地区过滤白名单（内联菜单使用）
func (e *Engine) ToggleRegion(chatID int64, region string) []string {
	region = strings.ToUpper(strings.TrimSpace(region))
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg := e.getOrCreateChatLocked(chatID)
	var newRegions []string
	found := false
	for _, r := range cfg.QuickRegions {
		if r == region {
			found = true
			continue
		}
		newRegions = append(newRegions, r)
	}
	if !found {
		newRegions = append(newRegions, region)
	}
	cfg.QuickRegions = newRegions
	e.notifySaveLocked()
	return cfg.QuickRegions
}

// EnableRegions 批量启用指定地区
func (e *Engine) EnableRegions(chatID int64, regions []string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg := e.getOrCreateChatLocked(chatID)
	regMap := make(map[string]bool)
	for _, r := range cfg.QuickRegions {
		regMap[strings.ToUpper(strings.TrimSpace(r))] = true
	}
	for _, r := range regions {
		cleaned := strings.ToUpper(strings.TrimSpace(r))
		if cleaned != "" {
			regMap[cleaned] = true
		}
	}
	var newRegions []string
	for r := range regMap {
		newRegions = append(newRegions, r)
	}
	cfg.QuickRegions = newRegions
	e.notifySaveLocked()
	return cfg.QuickRegions
}

// DisableRegions 批量禁用指定地区
func (e *Engine) DisableRegions(chatID int64, regions []string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg := e.getOrCreateChatLocked(chatID)
	disMap := make(map[string]bool)
	for _, r := range regions {
		cleaned := strings.ToUpper(strings.TrimSpace(r))
		if cleaned != "" {
			disMap[cleaned] = true
		}
	}
	var newRegions []string
	for _, r := range cfg.QuickRegions {
		if !disMap[strings.ToUpper(strings.TrimSpace(r))] {
			newRegions = append(newRegions, r)
		}
	}
	cfg.QuickRegions = newRegions
	e.notifySaveLocked()
	return cfg.QuickRegions
}

// SetRegions 设置指定的地区白名单列表（传入 nil 或空切片即恢复为“默认全开全部地区”）
func (e *Engine) SetRegions(chatID int64, regions []string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg := e.getOrCreateChatLocked(chatID)
	if len(regions) == 0 {
		cfg.QuickRegions = nil
	} else {
		var cleaned []string
		seen := make(map[string]bool)
		for _, r := range regions {
			c := strings.ToUpper(strings.TrimSpace(r))
			if c != "" && !seen[c] {
				seen[c] = true
				cleaned = append(cleaned, c)
			}
		}
		cfg.QuickRegions = cleaned
	}
	e.notifySaveLocked()
}

// SetQuickMaxPrice 设置快捷价格上限（内联菜单使用）
func (e *Engine) SetQuickMaxPrice(chatID int64, maxPrice float64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	cfg := e.getOrCreateChatLocked(chatID)
	cfg.QuickMaxPrice = maxPrice
	e.notifySaveLocked()
}

// GetAllSubscribedChats 获取所有已白名单授权且开启订阅的 Chat ID
func (e *Engine) GetAllSubscribedChats() []int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var chats []int64
	now := time.Now()
	for id, cfg := range e.chats {
		isAuth := cfg.Authorized || (e.adminID != 0 && id == e.adminID)
		if isAuth && cfg.Subscribed && (cfg.MutedUntil.IsZero() || now.After(cfg.MutedUntil)) {
			chats = append(chats, id)
		}
	}
	return chats
}

// Evaluate 判断某个 Chat 是否应该接收给定的实体通知
func (e *Engine) Evaluate(chatID int64, p *ItemPayload) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	cfg, ok := e.chats[chatID]
	if !ok {
		return false
	}

	// 0. 必须是已白名单授权（或者是管理员）
	isAuth := cfg.Authorized || (e.adminID != 0 && chatID == e.adminID)
	if !isAuth {
		return false
	}

	// 1. 检查订阅总开关与静音
	if !cfg.Subscribed {
		return false
	}
	if !cfg.MutedUntil.IsZero() && time.Now().Before(cfg.MutedUntil) {
		return false
	}

	// 2. 检查快捷地区过滤（如果有设置）
	if len(cfg.QuickRegions) > 0 && p.Region != "" {
		matched := false
		pReg := strings.ToUpper(p.Region)
		for _, r := range cfg.QuickRegions {
			if r == pReg {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// 3. 检查快捷限价
	if cfg.QuickMaxPrice > 0 && p.Price > cfg.QuickMaxPrice {
		return false
	}

	// 4. 检查自定义高级过滤规则（包含正则）
	if len(cfg.compiledRules) == 0 {
		// 无自定义高级规则时，通过基础过滤即可推送
		return true
	}

	// 若配置了自定义高级规则，至少需命中其中一条规则才进行推送 (OR 关系)
	for _, rule := range cfg.compiledRules {
		if rule.Match(p) {
			return true
		}
	}

	return false
}

func (e *Engine) notifySaveLocked() {
	if e.onSaveNeeded == nil {
		return
	}
	// 深拷贝快照，保证并发只读安全
	snapshot := make(map[int64]*ChatConfig, len(e.chats))
	for k, v := range e.chats {
		copyItem := *v
		copyItem.Rules = append([]Rule(nil), v.Rules...)
		copyItem.QuickRegions = append([]string(nil), v.QuickRegions...)
		snapshot[k] = &copyItem
	}

	// 放入单协程队列；若已有待处理快照，挤出旧快照放入最新快照，彻底杜绝乱序写和磁盘风暴
	select {
	case e.saveChan <- snapshot:
	default:
		select {
		case <-e.saveChan:
		default:
		}
		select {
		case e.saveChan <- snapshot:
		default:
		}
	}
}
