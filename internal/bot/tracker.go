package bot

import (
	"sync"
	"time"

	"narwhal-monitor/internal/storage"
)

// CardTracker 跟踪各母机卡片在各个 Chat 中的发送记录，支撑动态编辑与智能分流
type CardTracker struct {
	mu    sync.RWMutex
	cards map[string]*storage.TrackedMachineMsg // machineKey -> TrackedMachineMsg
}

// NewCardTracker 创建卡片消息生命周期跟踪器
func NewCardTracker(cards map[string]*storage.TrackedMachineMsg) *CardTracker {
	if cards == nil {
		cards = make(map[string]*storage.TrackedMachineMsg)
	}
	return &CardTracker{
		cards: cards,
	}
}

// ShouldEdit 判断针对某母机与指定会话，当前是否应当编辑旧消息
// 返回 targetMsgID 与 shouldEdit (true: 编辑旧消息; false: 推送全新卡片)
func (t *CardTracker) ShouldEdit(machineKey string, chatID int64, isRestock bool) (int, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	card, ok := t.cards[machineKey]
	if !ok || card == nil || card.MsgIDs == nil {
		return 0, false
	}

	msgID, hasMsg := card.MsgIDs[chatID]
	if !hasMsg || msgID <= 0 {
		return 0, false
	}

	if isRestock {
		// 补货场景：若距离上次推送在 30 分钟窗口内，编辑旧卡片；超过 30 分钟视为新周期，发送新卡片
		if time.Since(card.SentAt) <= 30*time.Minute {
			return msgID, true
		}
		return 0, false
	}

	// 纯库存减少或售罄场景：只要旧卡片存在，始终原地编辑
	return msgID, true
}

// RecordPush 记录或更新全新发送的卡片消息 ID
func (t *CardTracker) RecordPush(machineKey string, chatID int64, msgID int, stocks map[string]int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	card, ok := t.cards[machineKey]
	if !ok || card == nil {
		card = &storage.TrackedMachineMsg{
			MachineKey: machineKey,
			MsgIDs:     make(map[int64]int),
			PlanStocks: make(map[string]int),
		}
		t.cards[machineKey] = card
	}

	card.SentAt = time.Now()
	if card.MsgIDs == nil {
		card.MsgIDs = make(map[int64]int)
	}
	card.MsgIDs[chatID] = msgID

	if stocks != nil {
		if card.PlanStocks == nil {
			card.PlanStocks = make(map[string]int)
		}
		for k, v := range stocks {
			card.PlanStocks[k] = v
		}
	}
}

// RecordEdit 记录原地编辑旧消息
func (t *CardTracker) RecordEdit(machineKey string, chatID int64, stocks map[string]int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	card, ok := t.cards[machineKey]
	if !ok || card == nil {
		return
	}

	card.LastEditAt = time.Now()
	if stocks != nil {
		if card.PlanStocks == nil {
			card.PlanStocks = make(map[string]int)
		}
		for k, v := range stocks {
			card.PlanStocks[k] = v
		}
	}
}

// InvalidateMessage 当 Telegram 反馈消息不存在或已被删除时，清理该卡片记录
func (t *CardTracker) InvalidateMessage(machineKey string, chatID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	card, ok := t.cards[machineKey]
	if ok && card != nil && card.MsgIDs != nil {
		delete(card.MsgIDs, chatID)
	}
}

// CleanExpired 清理超过指定时间未活动的历史记录
func (t *CardTracker) CleanExpired(maxAge time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	for k, card := range t.cards {
		if card == nil {
			delete(t.cards, k)
			continue
		}
		refTime := card.LastEditAt
		if refTime.IsZero() {
			refTime = card.SentAt
		}
		if !refTime.IsZero() && now.Sub(refTime) > maxAge {
			delete(t.cards, k)
		}
	}
}

// GetCards 获取内部卡片快照供持久化（执行深拷贝，切断外部引用，杜绝并发 map 读写 panic）
func (t *CardTracker) GetCards() map[string]*storage.TrackedMachineMsg {
	t.mu.Lock()
	defer t.mu.Unlock()

	// 顺便淘汰 7 天以上无活动的旧卡片记录
	now := time.Now()
	const maxAge = 7 * 24 * time.Hour
	for k, card := range t.cards {
		if card == nil {
			delete(t.cards, k)
			continue
		}
		refTime := card.LastEditAt
		if refTime.IsZero() {
			refTime = card.SentAt
		}
		if !refTime.IsZero() && now.Sub(refTime) > maxAge {
			delete(t.cards, k)
		}
	}

	result := make(map[string]*storage.TrackedMachineMsg, len(t.cards))
	for k, v := range t.cards {
		if v == nil {
			continue
		}
		item := &storage.TrackedMachineMsg{
			MachineKey: v.MachineKey,
			SentAt:     v.SentAt,
			LastEditAt: v.LastEditAt,
			IsSoldOut:  v.IsSoldOut,
			MsgIDs:     make(map[int64]int, len(v.MsgIDs)),
			PlanStocks: make(map[string]int, len(v.PlanStocks)),
		}
		for ck, cv := range v.MsgIDs {
			item.MsgIDs[ck] = cv
		}
		for sk, sv := range v.PlanStocks {
			item.PlanStocks[sk] = sv
		}
		result[k] = item
	}
	return result
}
