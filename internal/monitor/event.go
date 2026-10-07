package monitor

import (
	"time"

	"narwhal-monitor/internal/api"
	"narwhal-monitor/internal/filter"
)

// EventType 定义监控事件类型
type EventType string

const (
	EventPlanNew     EventType = "plan_new"     // 官方新套餐上架
	EventPlanRestock EventType = "plan_restock" // 官方套餐补货
	EventPlanUpdate  EventType = "plan_update"  // 官方套餐库存扣减或售罄 (用于消息原地编辑)
)

// Event 是监控分发的统一事件对象
type Event struct {
	Type           EventType
	Payload        filter.ItemPayload
	MachineKey     string
	Plan           *api.PublicPlan
	TriggeredPlans []api.PublicPlan
	OtherPlans     []api.PublicPlan
	IsEditOnly     bool // 纯库存扣减/售罄变动，仅原地更新旧卡片，不发新卡片
	Timestamp      time.Time
}
