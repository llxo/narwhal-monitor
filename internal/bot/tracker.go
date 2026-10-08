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
func (t *CardTracker) ShouldEdit(machineKey string, chatID int64, isRestock bool, isNew bool, isEditOnly bool, newStocks map[string]int) (int, bool) {
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

	// 纯库存减少或售罄场景：始终原地编辑
	if isEditOnly {
		return msgID, true
	}

	// 全新套餐首发：发送新卡片
	if isNew {
		return 0, false
	}

	// 补货场景
	if isRestock {
		// 母机此前未售罄（持续有货）：退货或库存微调一律原地编辑，不发新消息
		if !card.IsSoldOut {
			return msgID, true
		}

		// 单次大批量放量 (增加 >= 5 台)：发送新卡片
		totalAdded := 0
		if newStocks != nil && card.PlanStocks != nil {
			for pid, nStock := range newStocks {
				oStock := card.PlanStocks[pid]
				if nStock > oStock && oStock >= 0 {
					totalAdded += (nStock - oStock)
				} else if oStock == 0 && (nStock > 0 || nStock == -1) {
					if nStock == -1 {
						totalAdded += 10
					} else {
						totalAdded += nStock
					}
				}
			}
		}
		if totalAdded >= 5 {
			return 0, false
		}

		// 售罄 30 分钟防抖：退货或超时释放优先原地编辑旧卡片
		soldOutRef := card.SoldOutAt
		if soldOutRef.IsZero() {
			soldOutRef = card.LastEditAt
			if soldOutRef.IsZero() {
				soldOutRef = card.SentAt
			}
		}
		if !soldOutRef.IsZero() && time.Since(soldOutRef) < 30*time.Minute {
			return msgID, true
		}

		// 彻底断货超 30 分钟后补货：发送新卡片
		return 0, false
	}

	return msgID, true
}

// RecordPush 记录全新发送的卡片消息
func (t *CardTracker) RecordPush(machineKey string, chatID int64, msgID int, stocks map[string]int, isSoldOut bool) {
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
	card.IsSoldOut = isSoldOut
	if isSoldOut {
		card.SoldOutAt = time.Now()
	} else {
		card.SoldOutAt = time.Time{}
	}

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
func (t *CardTracker) RecordEdit(machineKey string, chatID int64, stocks map[string]int, isSoldOut bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	card, ok := t.cards[machineKey]
	if !ok || card == nil {
		return
	}

	card.LastEditAt = time.Now()

	// 状态机演化：
	if isSoldOut {
		if !card.IsSoldOut {
			// 从在售状态转变为全盘售罄状态
			card.IsSoldOut = true
			card.SoldOutAt = time.Now()
		}
	} else {
		// 当前有货，清除售罄状态
		card.IsSoldOut = false
		card.SoldOutAt = time.Time{}
	}

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
			SoldOutAt:  v.SoldOutAt,
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
