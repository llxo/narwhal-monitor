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

// Poller 定时拉取并计算差分事件，内置限流保护
type Poller struct {
	client       *api.Client
	store        *storage.Store
	state        *storage.MonitorState
	mu           sync.Mutex
	interval     time.Duration
	eventHandler func(event Event)
	isReady      bool
	backoffUntil time.Time // 限速退避截止时间
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
			PlanStocks: make(map[string]int),
		}
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

	// 异步持久化当前状态快照
	if err := p.store.SaveState(p.state); err != nil {
		log.Printf("[监控警告] 保存状态失败: %v", err)
	}
}

type triggeredPlanInfo struct {
	plan      api.PublicPlan
	isNew     bool
	isRestock bool
	stock     int
}

func (p *Poller) diffPlans(plans []api.PublicPlan) {
	currentStockMap := make(map[string]int, len(plans))

	// 按机器归类所有公开套餐，供卡片展示同机器其他可选套餐
	machinePlansMap := make(map[string][]api.PublicPlan, len(plans))
	for _, plan := range plans {
		mKey := plan.MachineID
		if mKey == "" {
			mKey = plan.MachineName
		}
		machinePlansMap[mKey] = append(machinePlansMap[mKey], plan)
	}

	triggeredByMachine := make(map[string][]triggeredPlanInfo)
	var machineOrder []string

	for _, plan := range plans {
		currentStock := plan.Remaining
		if plan.SoldOut || plan.RamInsufficient {
			currentStock = 0
		} else if currentStock == 0 && !plan.SoldOut {
			// 0 且 sold_out 为 false 代表不限数量
			currentStock = -1
		}
		currentStockMap[plan.ID] = currentStock

		// 如果处于冷启动（首次加载且未记录过历史），只建立索引不触发通知
		if !p.isReady && len(p.state.PlanStocks) == 0 {
			continue
		}

		oldStock, existed := p.state.PlanStocks[plan.ID]
		isStockRestored := false
		isNewPlan := false

		if !existed {
			// 新上架套餐，且有货
			if currentStock != 0 {
				isNewPlan = true
			}
		} else if oldStock == 0 && currentStock != 0 {
			// 之前缺货/售罄，现在恢复有库存 (补货)
			isStockRestored = true
		}

		if isNewPlan || isStockRestored {
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
				isRestock: isStockRestored,
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
		isAllNew := true
		minPrice := items[0].plan.PriceMonthly
		hasRestock := false

		for _, item := range items {
			trigPlans = append(trigPlans, item.plan)
			trigIDs = append(trigIDs, item.plan.ID)
			trigNames = append(trigNames, item.plan.Name)
			if !item.isNew {
				isAllNew = false
			}
			if item.isRestock {
				hasRestock = true
			}
			if item.plan.PriceMonthly < minPrice {
				minPrice = item.plan.PriceMonthly
			}
		}

		evtType := EventPlanRestock
		if isAllNew {
			evtType = EventPlanNew
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
			Plan:           &firstPlan,
			TriggeredPlans: trigPlans,
			OtherPlans:     otherPlans,
			Timestamp:      time.Now(),
		})
	}

	p.state.PlanStocks = currentStockMap
}

func (p *Poller) emitEvent(evt Event) {
	if p.eventHandler != nil && p.isReady {
		p.eventHandler(evt)
	}
}
