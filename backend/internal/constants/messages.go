package constants

// 接口返回文案、日志文案、错误提示文案统一维护。
const (
	MsgOK            = "ok"
	MsgCreateSuccess = "创建成功"
	MsgUpdateSuccess = "更新成功"
	MsgDeleteSuccess = "删除成功"
	MsgLoginSuccess  = "登录成功"
	MsgLogoutSuccess = "退出登录成功"
	MsgRegisterOK    = "注册成功"

	MsgRechargeOK   = "充值成功"
	MsgBuyPackageOK = "时长包购买成功"
	MsgReserveOK    = "预约成功"
	MsgCheckInOK    = "开机成功"
	MsgRenewOK      = "续费成功"
	MsgCheckoutOK   = "下机成功"
	MsgDrawOK       = "抽签分组完成"

	MsgRepairCreateOK = "报修登记成功"
	MsgRepairCloseOK  = "报修已关闭，机位恢复空闲"
)

// 报修相关错误提示文案（message 中带实体名/字段名/角色名，service/handler 层层包装）。
const (
	MsgRepairReasonRequired = "报修原因不能为空（实体：repair_record，字段：reason）"
	MsgRepairResultRequired = "处理结果不能为空（实体：repair_record，字段：handle_result）"
	MsgRepairStationFault   = "仅故障机位可以登记报修（实体：station，字段：status）"
	MsgRepairToIdleOnly     = "存在待处理报修时，机位仅允许恢复为空闲（实体：station，字段：status）"
	MsgRepairRoleReject     = "当前角色无权操作报修闭环（实体：repair_record，允许角色：管理员/店员）"
)

// 日志文案模板（非格式化部分）。
const (
	LogUserLogin     = "用户登录"
	LogUserRegister  = "用户注册"
	LogStationChange = "机位状态变更"
	LogRecharge      = "会员充值"
	LogReservation   = "机位预约"
	LogSessionStart  = "会员上机"
	LogSessionEnd    = "会员下机"
	LogTournament    = "赛事操作"
	LogAuditAction   = "审计动作"
)
