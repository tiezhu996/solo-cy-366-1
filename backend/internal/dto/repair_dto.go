package dto

// CreateRepairReq 登记报修请求（管理员/店员标记机位故障时填写原因；机位 ID 来自路径参数）。
type CreateRepairReq struct {
	Reason string `json:"reason" binding:"required,max=500"`
}

// CloseRepairReq 关闭报修请求（机位恢复空闲时填写处理结果）。
type CloseRepairReq struct {
	HandleResult string `json:"handle_result" binding:"required,max=500"`
}

// RepairQuery 报修记录查询参数。
type RepairQuery struct {
	Page      int    `form:"page" binding:"omitempty,min=1"`
	PageSize  int    `form:"page_size" binding:"omitempty,min=1,max=100"`
	StationID uint   `form:"station_id" binding:"omitempty,min=1"`
	Status    string `form:"status" binding:"omitempty,oneof=pending closed"`
}
