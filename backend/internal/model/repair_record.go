package model

import "time"

// RepairRecord 机位报修记录：标记故障时登记（pending），恢复空闲时关闭（closed）。
// 同一机位同时只允许一条 pending 记录，由唯一索引 uk_repair_open_station 与事务行锁共同保证。
type RepairRecord struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	StationID    uint       `gorm:"index;not null" json:"station_id"`
	Reason       string     `gorm:"type:varchar(500);not null" json:"reason"`    // 故障原因（登记时必填）
	Status       string     `gorm:"size:16;default:pending;index" json:"status"` // pending / closed
	ReportUserID uint       `gorm:"index" json:"report_user_id"`                 // 登记操作人
	ReportByName string     `gorm:"size:64" json:"report_by_name"`               // 登记操作人用户名快照
	ReportRole   string     `gorm:"size:16" json:"report_role"`                  // 登记操作角色 admin/staff
	ReportedAt   time.Time  `gorm:"index" json:"reported_at"`                    // 登记时间
	HandleResult string     `gorm:"type:varchar(500)" json:"handle_result"`      // 处理结果（恢复时必填）
	HandleUserID uint       `gorm:"index" json:"handle_user_id"`                 // 恢复操作人
	HandleByName string     `gorm:"size:64" json:"handle_by_name"`               // 恢复操作人用户名快照
	HandleRole   string     `gorm:"size:16" json:"handle_role"`                  // 恢复操作角色 admin/staff
	HandledAt    *time.Time `json:"handled_at"`                                  // 恢复/关闭时间
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// TableName 指定表名。
func (RepairRecord) TableName() string { return "repair_records" }
