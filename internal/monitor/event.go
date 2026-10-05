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
)

// Event 是监控分发的统一事件对象
type Event struct {
	Type           EventType
	Payload        filter.ItemPayload
	Plan           *api.PublicPlan
	TriggeredPlans []api.PublicPlan
	OtherPlans     []api.PublicPlan
	Timestamp      time.Time
}
