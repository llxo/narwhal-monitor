package monitor

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"narwhal-monitor/internal/api"
	"narwhal-monitor/internal/filter"
	"narwhal-monitor/internal/storage"
)

// CardTrackerProvider 接口供持久化时同步卡片追踪信息
type CardTrackerProvider interface {
	GetCards() map[string]*storage.TrackedMachineMsg
}

// Poller 定时拉取并计算差分事件，内置限流保护
type Poller struct {
	client       *api.Client
	store        *storage.Store
	state        *storage.MonitorState
	cardProvider CardTrackerProvider
	mu           sync.Mutex
	interval     time.Duration
	eventHandler func(event Event)
	isReady      bool
	backoffUntil time.Time // 限速退避截止时间
}

// SetCardProvider 设置卡片消息追踪提供器
func (p *Poller) SetCardProvider(provider CardTrackerProvider) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cardProvider = provider
}

// NewPoller 创建监控轮询器
func NewPoller(client *api.Client, store *storage.Store, interval time.Duration, onEvent func(event Event)) (*Poller, error) {
	if interval < 12*time.Second {
		// 平台 /plans 接口限速 10次/分，强制保证安全间隔
		interval = 15 * time.Second
	}

	state, err := store.LoadState()
	if err != nil {
		log.Printf("[警告] 加载上次监控状态失败，将初始化新状态: %v", err)
		state = &storage.MonitorState{
			PlanStocks:   make(map[string]int),
			KnownPlanIDs: make(map[string]bool),
			TrackedCards: make(map[string]*storage.TrackedMachineMsg),
		}
	}
	if state.PlanStocks == nil {
		state.PlanStocks = make(map[string]int)
	}
	if state.KnownPlanIDs == nil {
		state.KnownPlanIDs = make(map[string]bool)
	}
	if state.TrackedCards == nil {
		state.TrackedCards = make(map[string]*storage.TrackedMachineMsg)
	}

	return &Poller{
		client:       client,
		store:        store,
		state:        state,
		interval:     interval,
		eventHandler: onEvent,
	}, nil
}

// Start 启动后台轮询 Goroutine，直到 ctx 取消
func (p *Poller) Start(ctx context.Context) {
	log.Printf("[监控] 启动轮询引擎，间隔: %v", p.interval)

	// 启动立即执行一次初次扫描
	p.poll(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[监控] 收到停止信号，监控轮询已安全退出")
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

// poll 执行单次全量检测与差分计算
func (p *Poller) poll(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// 若处于平台限速惩罚期，暂停请求避免封禁时间叠加延长
	if time.Now().Before(p.backoffUntil) {
		log.Printf("[监控限速] 处于平台限速退避冷却中，跳过本轮抓取 (冷却至 %s)", p.backoffUntil.Format("15:04:05"))
		return
	}

	// 轮询官方套餐与库存
	plans, err := p.client.GetPlans(ctx)
	if err != nil {
		var rle *api.RateLimitError
		if errors.As(err, &rle) {
			p.backoffUntil = time.Now().Add(rle.RetryAfter)
			log.Printf("[监控限速] %v，自动进入退避状态，冷却至 %s", rle, p.backoffUntil.Format("15:04:05"))
		} else {
			log.Printf("[监控错误] 获取套餐失败: %v", err)
		}
	} else {
		p.diffPlans(plans)
	}

	// 标记冷启动已完成，之后的变动均会触发真实通知推送
	if !p.isReady {
		p.isReady = true
		log.Println("[监控] 初始数据快照建立完成，开始监听新事件推送...")
	}

	if p.cardProvider != nil {
		p.state.TrackedCards = p.cardProvider.GetCards()
	}

	// 异步持久化当前状态快照
	if err := p.store.SaveState(p.state); err != nil {
		log.Printf("[监控警告] 保存状态失败: %v", err)
	}
}

type triggeredPlanInfo struct {
	plan      api.PublicPlan
	isNew     bool
	isRestock bool
	isReduced bool
	stock     int
}

func (p *Poller) diffPlans(plans []api.PublicPlan) {
	// 按机器归类所有公开套餐，供卡片展示同机器其他可选套餐
	machinePlansMap := make(map[string][]api.PublicPlan, len(plans))
	for _, plan := range plans {
		mKey := plan.MachineID
		if mKey == "" {
			mKey = plan.MachineName
		}
		machinePlansMap[mKey] = append(machinePlansMap[mKey], plan)
	}

	// 如果处于冷启动（首次加载且未记录过历史），只建立索引不触发通知
	if !p.isReady && len(p.state.KnownPlanIDs) == 0 && len(p.state.PlanStocks) == 0 {
		for _, plan := range plans {
			currentStock := plan.NormalizedRemaining()
			p.state.PlanStocks[plan.ID] = currentStock
			p.state.KnownPlanIDs[plan.ID] = true
		}
		return
	}

	triggeredByMachine := make(map[string][]triggeredPlanInfo)
	var machineOrder []string

	for _, plan := range plans {
		currentStock := plan.NormalizedRemaining()

		oldStock, existedInStocks := p.state.PlanStocks[plan.ID]
		isKnown := p.state.KnownPlanIDs[plan.ID]

		isNewPlan := false
		isRestock := false
		isReduced := false

		if !isKnown {
			// 历史从未出现过的全新套餐首发
			if currentStock != 0 {
				isNewPlan = true
			}
		} else {
			// 历史上见过的老套餐
			if !existedInStocks {
				// 之前下架或母机掉线过，现重新出现且有库存，按补货处理
				if currentStock != 0 {
					isRestock = true
				}
			} else {
				// 历史在库，计算差分
				if oldStock == 0 && currentStock != 0 {
					// 之前缺货售罄，现在恢复有库存 (补货)
					isRestock = true
				} else if oldStock > 0 && currentStock > oldStock {
					// 库存数量增加 (补货)
					isRestock = true
				} else if oldStock > 0 && (currentStock < oldStock || currentStock == 0) {
					// 库存数量减少或变为售罄 (扣减/售罄，供卡片原地编辑)
					isReduced = true
				}
			}
		}

		if isNewPlan || isRestock || isReduced {
			mKey := plan.MachineID
			if mKey == "" {
				mKey = plan.MachineName
			}
			if len(triggeredByMachine[mKey]) == 0 {
				machineOrder = append(machineOrder, mKey)
			}
			triggeredByMachine[mKey] = append(triggeredByMachine[mKey], triggeredPlanInfo{
				plan:      plan,
				isNew:     isNewPlan,
				isRestock: isRestock,
				isReduced: isReduced,
				stock:     currentStock,
			})
		}
	}

	// 按宿主机逐台发出聚合通知（同一母机在同一次轮询中只发送一条聚合消息，避免多套餐同时变更时刷屏）
	for _, mKey := range machineOrder {
		items := triggeredByMachine[mKey]
		if len(items) == 0 {
			continue
		}

		var trigPlans []api.PublicPlan
		var trigIDs []string
		var trigNames []string
		hasNew := false
		hasRestock := false
		hasReduced := false
		minPrice := items[0].plan.PriceMonthly

		for _, item := range items {
			trigPlans = append(trigPlans, item.plan)
			trigIDs = append(trigIDs, item.plan.ID)
			trigNames = append(trigNames, item.plan.Name)
			if item.isNew {
				hasNew = true
			}
			if item.isRestock {
				hasRestock = true
			}
			if item.isReduced {
				hasReduced = true
			}
			if item.plan.PriceMonthly < minPrice {
				minPrice = item.plan.PriceMonthly
			}
		}

		var evtType EventType
		var isEditOnly bool

		if hasNew {
			evtType = EventPlanNew
			isEditOnly = false
		} else if hasRestock {
			evtType = EventPlanRestock
			isEditOnly = false
		} else if hasReduced {
			evtType = EventPlanUpdate
			isEditOnly = true
		} else {
			continue
		}

		// 收集该机器下未触发的其他套餐
		var otherPlans []api.PublicPlan
		trigIDMap := make(map[string]bool, len(trigIDs))
		for _, id := range trigIDs {
			trigIDMap[id] = true
		}
		for _, other := range machinePlansMap[mKey] {
			if !trigIDMap[other.ID] {
				otherPlans = append(otherPlans, other)
			}
		}

		// 组合描述文本（机器描述 + 所有触发套餐描述）
		firstPlan := items[0].plan
		fullDesc := firstPlan.MachineDescription
		for _, item := range items {
			if item.plan.Description != "" {
				if fullDesc != "" {
					fullDesc += " " + item.plan.Description
				} else {
					fullDesc = item.plan.Description
				}
			}
		}

		payload := filter.ItemPayload{
			Type:        "plan",
			ID:          firstPlan.ID,
			Title:       strings.Join(trigNames, " "),
			Description: fullDesc,
			Region:      firstPlan.MachineRegion,
			Price:       minPrice,
			CPU:         firstPlan.CPU,
			RamMB:       firstPlan.RamMB,
			DiskGB:      firstPlan.DiskGB,
			Bandwidth:   firstPlan.BandwidthMbps,
			MachineName: firstPlan.MachineName,
			Tags:        firstPlan.MachineTags,
			IsRestock:   hasRestock,
			Remaining:   items[0].stock,
		}

		p.emitEvent(Event{
			Type:           evtType,
			Payload:        payload,
			MachineKey:     mKey,
			Plan:           &firstPlan,
			TriggeredPlans: trigPlans,
			OtherPlans:     otherPlans,
			IsEditOnly:     isEditOnly,
			Timestamp:      time.Now(),
		})
	}

	// 增量更新历史快照（保留未返回套餐的最后已知库存，杜绝网络抖动/下架重上架导致的虚假补货刷屏）
	for _, plan := range plans {
		currentStock := plan.NormalizedRemaining()
		p.state.PlanStocks[plan.ID] = currentStock
		p.state.KnownPlanIDs[plan.ID] = true
	}
}

func (p *Poller) emitEvent(evt Event) {
	if p.eventHandler != nil && p.isReady {
		p.eventHandler(evt)
	}
}
