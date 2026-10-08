package api

// ApiResponse 是平台 API 的标准返回格式包装
type ApiResponse[T any] struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data T      `json:"data"`
}

// UserResponse 是 /user/me 接口返回的用户个人资料
type UserResponse struct {
	ID               string  `json:"id"`
	Email            string  `json:"email"`
	Role             string  `json:"role"`
	AvailableBalance float64 `json:"available_balance"`
	EscrowBalance    float64 `json:"escrow_balance"`
	NotifyVia        string  `json:"notify_via"`
	NotifyLang       string  `json:"notify_lang"`
	TGChatID         int64   `json:"tg_chat_id"`
	CreatedAt        int64   `json:"created_at"`
}

// LoginResult 是 /auth/login 返回的登录鉴权结果
type LoginResult struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
}

// PlansData 是 /plans 接口返回的 data 字段
type PlansData struct {
	Plans []PublicPlan `json:"plans"`
	Total int          `json:"total"`
}

// OSImage 代表支持的系统镜像
type OSImage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// StockUnlimited 表示库存充裕 / 不限量
const StockUnlimited = -1

// PublicPlan 代表公开市场上的主机套餐信息
type PublicPlan struct {
	ID                 string    `json:"id"`
	MachineID          string    `json:"machine_id"`
	OwnerID            string    `json:"owner_id"`
	Name               string    `json:"name"`
	Description        string    `json:"description"`
	CPU                int       `json:"cpu"`
	RamMB              int       `json:"ram_mb"`
	DiskGB             int       `json:"disk_gb"`
	BandwidthMbps      int       `json:"bandwidth_mbps"`
	MonthlyTrafficGB   int       `json:"monthly_traffic_gb"`
	PriceMonthly       float64   `json:"price_monthly"`
	Remaining          int       `json:"remaining"`
	SoldOut            bool      `json:"sold_out"`
	RamInsufficient    bool      `json:"ram_insufficient"`
	MachineName        string    `json:"machine_name"`
	MachineRegion      string    `json:"machine_region"`
	MachineStatus      string    `json:"machine_status"`
	MachineTags        []string  `json:"machine_tags"`
	MachineDescription string    `json:"machine_description"`
	MachineVMType      string    `json:"machine_vm_type"`
	MachineExpiresAt   int64     `json:"machine_expires_at"`
	MachineSLA         float64   `json:"machine_sla"`
	MachineCreatedAt   int64     `json:"machine_created_at"`
	SupportedOS        []OSImage `json:"supported_os"`
	Status             string    `json:"status"`
	CreatedAt          int64     `json:"created_at"`
	UpdatedAt          int64     `json:"updated_at"`
}

// NormalizedRemaining 统一返回标准化的可用库存：
// - 若已售罄 (SoldOut) 或母机内存不足 (RamInsufficient)，返回 0
// - 若 Remaining 为 0 且未售罄，表示不限量 / 充足库存，返回 StockUnlimited (-1)
// - 否则返回实际剩余数量
func (p PublicPlan) NormalizedRemaining() int {
	if p.SoldOut || p.RamInsufficient {
		return 0
	}
	if p.Remaining == 0 && !p.SoldOut {
		return StockUnlimited
	}
	return p.Remaining
}



