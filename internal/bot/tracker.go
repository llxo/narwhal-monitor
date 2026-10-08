package bot

import (
	"sync"
	"time"

	"narwhal-monitor/internal/api"
	"narwhal-monitor/internal/storage"
)

const (
	// MaxCardAge 旧卡片最大生命周期（24 小时）。超过此时间若发生补货，旧消息已沉底，强制发送新卡片
	MaxCardAge = 24 * time.Hour
	// SoldOutDebounceWindow 售罄后的防抖窗口（30 分钟）。在此窗口内的微量补货（退货/超时释放）优先原地编辑
	SoldOutDebounceWindow = 30 * time.Minute
	// BatchRestockThreshold 单次大批量放量阈值。累计增加 >= 3 台视为重大补货，触发新卡片推送
	BatchRestockThreshold = 3
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

	// 1. 纯库存减少或售罄场景：始终原地编辑旧卡片，绝不发送新消息打扰
	if isEditOnly {
		return msgID, true
	}

	// 2. 全新套餐首发：发送全新卡片，确保用户第一时间获悉
	if isNew {
		return 0, false
	}

	// 3. 补货场景决策
	if isRestock {
		// 3.1 旧卡片绝对存活时间兜底：若距首次推送已超过 24 小时，旧卡片在聊天窗口中早已沉底，发新卡片
		if !card.SentAt.IsZero() && time.Since(card.SentAt) > MaxCardAge {
			return 0, false
		}

		// 3.2 计算本次相较上次记录的库存总增量 (totalAdded)
		totalAdded := 0
		if newStocks != nil {
			for pid, nStock := range newStocks {
				oStock := 0
				if card.PlanStocks != nil {
					oStock = card.PlanStocks[pid]
				}
				if nStock == api.StockUnlimited {
					// 新库存为不限量 / 充裕状态
					if oStock != api.StockUnlimited {
						totalAdded += 10 // 从有限库存或售罄转为充裕，视为大批量放量
					}
				} else if nStock > 0 {
					// 新库存为明确数量 (>0)
					if oStock == api.StockUnlimited {
						// 从充裕转为有限具体数值，属于库存收紧/明确化，不计入新增增量
					} else if nStock > oStock {
						totalAdded += (nStock - oStock)
					}
				}
			}
		}

		// 3.3 单次大批量放量 (增加 >= 3 台)：无论是此前在售还是已售罄，均视为重大事件，发送新卡片
		if totalAdded >= BatchRestockThreshold {
			return 0, false
		}

		// 3.4 微量补货场景 (< 3 台，如 1~2 台退款或超时释放)：
		// 若母机此前未全盘售罄（持续有货）：退货或库存微调一律原地编辑，防止刷屏轰炸
		if !card.IsSoldOut {
			return msgID, true
		}

		// 3.5 售罄状态下的微量补货：检查售罄防抖窗口 (30 分钟)
		soldOutRef := card.SoldOutAt
		if soldOutRef.IsZero() {
			soldOutRef = card.LastEditAt
			if soldOutRef.IsZero() {
				soldOutRef = card.SentAt
			}
		}
		if !soldOutRef.IsZero() && time.Since(soldOutRef) < SoldOutDebounceWindow {
			// 售罄 30 分钟防抖期内：微量退货优先原地编辑旧卡片
			return msgID, true
		}

		// 彻底断货超 30 分钟后再度补货：发送新卡片
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

	// 保护全局基准时间戳：仅在首次建立卡片或大周期重新发送时才刷新 SentAt，
	// 避免单个新 Chat 加入增量推送时刷新整台母机的 SentAt，污染其他 Chat 的时间窗口
	if card.SentAt.IsZero() || time.Since(card.SentAt) > SoldOutDebounceWindow {
		card.SentAt = time.Now()
	}

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
