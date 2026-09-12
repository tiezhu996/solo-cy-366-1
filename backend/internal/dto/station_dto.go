package dto

// CreateStationReq 创建机位请求。
type CreateStationReq struct {
	Name         string  `json:"name" binding:"required,max=64"`
	Area         string  `json:"area" binding:"required,max=64"`
	StationType  string  `json:"station_type" binding:"required,oneof=seat box"`
	PricePerHour float64 `json:"price_per_hour" binding:"omitempty,min=0"`
	Description  string  `json:"description" binding:"omitempty,max=255"`
}

// UpdateStationReq 更新机位请求。
type UpdateStationReq struct {
	Name         string  `json:"name" binding:"omitempty,max=64"`
	Area         string  `json:"area" binding:"omitempty,max=64"`
	StationType  string  `json:"station_type" binding:"omitempty,oneof=seat box"`
	PricePerHour float64 `json:"price_per_hour" binding:"omitempty,min=0"`
	Description  string  `json:"description" binding:"omitempty,max=255"`
}

// UpdateStationStatusReq 机位状态变更请求。
// 标记故障时 reason 必填（生成待处理报修）；恢复空闲时 handle_result 必填（关闭报修）。
type UpdateStationStatusReq struct {
	Status       string `json:"status" binding:"required,oneof=idle using fault reserved"`
	Reason       string `json:"reason" binding:"omitempty,max=500"`        // 故障原因：status=fault 时必填
	HandleResult string `json:"handle_result" binding:"omitempty,max=500"` // 处理结果：fault->idle 时必填
}

// StationDetailResp 机位详情（含报修闭环信息：当前待处理报修与历史记录）。
// 字段使用 any 以保持 dto 层不依赖 model（与 PageResult 风格一致），由 service 层装配具体实体。
type StationDetailResp struct {
	Station       any   `json:"station"`
	OpenRepair    any   `json:"open_repair"`
	RepairRecords []any `json:"repair_records"`
}

// StationQuery 机位查询参数。
type StationQuery struct {
	Page     int    `form:"page" binding:"omitempty,min=1"`
	PageSize int    `form:"page_size" binding:"omitempty,min=1,max=100"`
	Area     string `form:"area"`
	Status   string `form:"status" binding:"omitempty,oneof=idle using fault reserved"`
}
