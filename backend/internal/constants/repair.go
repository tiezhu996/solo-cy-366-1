package constants

// RepairStatus 报修记录状态枚举（与机位故障状态机联动）。
const (
	RepairPending = "pending" // 待处理（机位处于故障）
	RepairClosed  = "closed"  // 已关闭（机位恢复空闲并填写处理结果）
)

// AllRepairStatus 所有报修状态。
var AllRepairStatus = []string{RepairPending, RepairClosed}

// IsValidRepairStatus 判断报修状态是否合法。
func IsValidRepairStatus(s string) bool {
	switch s {
	case RepairPending, RepairClosed:
		return true
	}
	return false
}
