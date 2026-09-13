package service

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/esportsbar/backend/internal/constants"
	"github.com/esportsbar/backend/internal/dto"
	"github.com/esportsbar/backend/internal/model"
	"github.com/esportsbar/backend/internal/repository"
	"github.com/esportsbar/backend/internal/util"
)

// 本文件为机位报修闭环的可重复测试（规则集见各用例名/子测试名）。
//
// 规则总览：
//  R1 空闲机位登记报修：机位 idle->fault，生成 pending 记录，原因/登记角色/登记时间正确
//  R2 填写处理结果恢复：机位 fault->idle，记录 pending->closed，结果/处理角色/处理时间正确
//  R3 同一机位仅允许一条待处理报修：重复登记返回 CodeRepairOpen，不新增记录
//  R4 仅故障机位可关闭：
//     - 非故障机位关闭     -> CodeConflict
//     - 故障且有待处理单   -> 正常关闭该单（R2）
//  R5 使用中/已预约机位不能登记故障：CodeConflict
//  R6 原因/结果必填（空白同样拒绝），且不落库
//  R7 闭环可循环；机位详情只展示最近 20 条历史，更多记录可通过分页列表查到
//  R8 原状态流转接口不得绕过报修闭环：idle->fault / fault->idle 均被拒绝；
//     原有非故障状态操作（reserved->idle）保持可用
//  R11 故障但无待处理报修单的机位（历史遗留脏数据）不再卡死：
//     填写处理结果后直接补录一条已关闭报修记录并恢复空闲；记录留存处理结果/角色/时间；
//     空白结果仍拒绝；恢复后可正常重新登记
//
// 稳定性：每个用例使用独立 DSN 的内存 sqlite 库（mode=memory），无外部依赖、
// 无端口、无随机数据，可连续反复运行；断言信息以「规则名」开头，失败即可指认被破坏的规则。

var repairDBSNSeq int64

// newRepairTestDB 为单个用例创建独立内存数据库。
func newRepairTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	n := atomic.AddInt64(&repairDBSNSeq, 1)
	dsn := fmt.Sprintf("file:repair_rule_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("[测试基建] 打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Station{}, &model.RepairRecord{}); err != nil {
		t.Fatalf("[测试基建] 建表失败: %v", err)
	}
	return db
}

type repairFixture struct {
	db         *gorm.DB
	repairSvc  *RepairService
	stationSvc *StationService
}

func newRepairFixture(t *testing.T) *repairFixture {
	t.Helper()
	db := newRepairTestDB(t)
	logger := newTestLogger()
	stationRepo := repository.NewStationRepository(db)
	repairRepo := repository.NewRepairRepository(db)
	return &repairFixture{
		db:         db,
		repairSvc:  NewRepairService(repairRepo, stationRepo, db, logger),
		stationSvc: NewStationService(stationRepo, repairRepo, logger),
	}
}

func (f *repairFixture) seedStation(t *testing.T, status string) *model.Station {
	t.Helper()
	st := &model.Station{Name: "A区-01", Area: "A区", StationType: "seat", PricePerHour: 8, Status: status}
	if err := f.db.Create(st).Error; err != nil {
		t.Fatalf("[测试基建] 造机位失败: %v", err)
	}
	return st
}

var ruleAdmin = Operator{UserID: 101, Username: "admin", Role: constants.RoleAdmin}
var ruleStaff = Operator{UserID: 202, Username: "clerk", Role: constants.RoleStaff}

// failRule 以规则名开头输出失败信息，便于指认被破坏的规则。
func failRule(t *testing.T, rule string, format string, args ...any) {
	t.Helper()
	t.Fatalf("[%s] %s", rule, fmt.Sprintf(format, args...))
}

func appErrorCode(t *testing.T, err error, rule string) int {
	t.Helper()
	var appErr *util.AppError
	if !errors.As(err, &appErr) {
		failRule(t, rule, "期望业务错误 *util.AppError，实际 %T: %v", err, err)
	}
	return appErr.Code
}

// assertPending 校验登记后的待处理记录快照（R1）。
func assertPending(t *testing.T, rule string, rec *model.RepairRecord, stationID uint, op Operator, reason string) {
	t.Helper()
	if rec.StationID != stationID {
		failRule(t, rule, "报修机位ID错误: got=%d want=%d", rec.StationID, stationID)
	}
	if rec.Status != constants.RepairPending {
		failRule(t, rule, "报修状态应为 pending: got=%s", rec.Status)
	}
	if rec.Reason != reason {
		failRule(t, rule, "故障原因错误: got=%q want=%q", rec.Reason, reason)
	}
	if rec.ReportUserID != op.UserID || rec.ReportByName != op.Username || rec.ReportRole != op.Role {
		failRule(t, rule, "登记操作人快照错误: got=(%d,%s,%s) want=(%d,%s,%s)",
			rec.ReportUserID, rec.ReportByName, rec.ReportRole, op.UserID, op.Username, op.Role)
	}
	if rec.ReportedAt.IsZero() {
		failRule(t, rule, "登记时间 reported_at 不应为零值")
	}
	if rec.HandleResult != "" || rec.HandledAt != nil {
		failRule(t, rule, "待处理记录不应已有处理结果/处理时间: result=%q handledAt=%v", rec.HandleResult, rec.HandledAt)
	}
}

// assertClosed 校验关闭后的记录快照（R2）。
func assertClosed(t *testing.T, rule string, rec *model.RepairRecord, op Operator, result string) {
	t.Helper()
	if rec.Status != constants.RepairClosed {
		failRule(t, rule, "报修状态应为 closed: got=%s", rec.Status)
	}
	if rec.HandleResult != result {
		failRule(t, rule, "处理结果错误: got=%q want=%q", rec.HandleResult, result)
	}
	if rec.HandleUserID != op.UserID || rec.HandleByName != op.Username || rec.HandleRole != op.Role {
		failRule(t, rule, "处理操作人快照错误: got=(%d,%s,%s) want=(%d,%s,%s)",
			rec.HandleUserID, rec.HandleByName, rec.HandleRole, op.UserID, op.Username, op.Role)
	}
	if rec.HandledAt == nil || rec.HandledAt.IsZero() {
		failRule(t, rule, "处理时间 handled_at 不应为零值")
	}
	if rec.HandledAt.Before(rec.ReportedAt) {
		failRule(t, rule, "时间顺序错误: handled_at(%s) 早于 reported_at(%s)",
			rec.HandledAt.Format("15:04:05.000"), rec.ReportedAt.Format("15:04:05.000"))
	}
}

func assertStationStatus(t *testing.T, rule string, db *gorm.DB, stationID uint, want string) {
	t.Helper()
	var fresh model.Station
	if err := db.First(&fresh, stationID).Error; err != nil {
		failRule(t, rule, "回读机位失败: %v", err)
	}
	if fresh.Status != want {
		failRule(t, rule, "机位状态错误: got=%s want=%s（%s）", fresh.Status, want, util.StatusText(want))
	}
}

func countPending(t *testing.T, rule string, db *gorm.DB, stationID uint) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.RepairRecord{}).
		Where("station_id = ? AND status = ?", stationID, constants.RepairPending).Count(&n).Error; err != nil {
		failRule(t, rule, "统计待处理报修失败: %v", err)
	}
	return n
}

// TestRepairRuleReportAndRecover R1+R2：空闲登记故障 -> 填写结果恢复空闲的完整闭环。
func TestRepairRuleReportAndRecover(t *testing.T) {
	const rule = "R1/R2 登记报修并恢复空闲"
	f := newRepairFixture(t)
	st := f.seedStation(t, constants.StationIdle)

	// R1 登记
	rec, station, err := f.repairSvc.Report(ruleAdmin, st.ID, "键盘失灵，鼠标断连")
	if err != nil {
		failRule(t, rule, "登记报修不应失败: %v", err)
	}
	assertPending(t, rule, rec, st.ID, ruleAdmin, "键盘失灵，鼠标断连")
	if station.Status != constants.StationFault {
		failRule(t, rule, "返回机位状态应为 fault: got=%s", station.Status)
	}
	assertStationStatus(t, rule, f.db, st.ID, constants.StationFault)
	if countPending(t, rule, f.db, st.ID) != 1 {
		failRule(t, rule, "库中待处理报修应为 1 条")
	}

	// R2 恢复（换店员操作，验证处理角色快照）
	closed, recovered, legacy, err := f.repairSvc.Close(ruleStaff, st.ID, "已更换键鼠并测试通过")
	if err != nil {
		failRule(t, rule, "关闭报修不应失败: %v", err)
	}
	if legacy {
		failRule(t, rule, "正常闭环不应走历史遗留补录路径")
	}
	assertClosed(t, rule, closed, ruleStaff, "已更换键鼠并测试通过")
	if recovered.Status != constants.StationIdle {
		failRule(t, rule, "返回机位状态应为 idle: got=%s", recovered.Status)
	}
	assertStationStatus(t, rule, f.db, st.ID, constants.StationIdle)
	if countPending(t, rule, f.db, st.ID) != 0 {
		failRule(t, rule, "关闭后不应存在待处理报修")
	}
}

// TestRepairRuleDuplicateRejected R3：同一机位重复登记被拒。
func TestRepairRuleDuplicateRejected(t *testing.T) {
	const rule = "R3 同一机位仅一条待处理报修"
	f := newRepairFixture(t)
	st := f.seedStation(t, constants.StationIdle)

	if _, _, err := f.repairSvc.Report(ruleAdmin, st.ID, "显示器花屏"); err != nil {
		failRule(t, rule, "首次登记不应失败: %v", err)
	}
	// 机位已处于 fault，换店员再次登记
	_, _, err := f.repairSvc.Report(ruleStaff, st.ID, "重复报修")
	if err == nil {
		failRule(t, rule, "重复登记必须被拒绝，但返回成功")
	}
	if code := appErrorCode(t, err, rule); code != constants.CodeRepairOpen {
		failRule(t, rule, "重复登记错误码应为 %d(CodeRepairOpen): got=%d", constants.CodeRepairOpen, code)
	}
	if n := countPending(t, rule, f.db, st.ID); n != 1 {
		failRule(t, rule, "拒绝重复登记后待处理记录仍应只有 1 条: got=%d", n)
	}
	var total int64
	f.db.Model(&model.RepairRecord{}).Count(&total)
	if total != 1 {
		failRule(t, rule, "拒绝重复登记不应写入任何记录: total=%d", total)
	}
	// 机位仍保持故障
	assertStationStatus(t, rule, f.db, st.ID, constants.StationFault)
}

// TestRepairRuleCloseGuards R4/R11：关闭操作的守卫与历史遗留故障自愈。
func TestRepairRuleCloseGuards(t *testing.T) {
	t.Run("R4b 非故障机位关闭被拒绝", func(t *testing.T) {
		const rule = "R4b 非故障机位不能关闭报修"
		f := newRepairFixture(t)
		st := f.seedStation(t, constants.StationIdle)
		_, _, _, err := f.repairSvc.Close(ruleAdmin, st.ID, "修好了")
		if err == nil {
			failRule(t, rule, "空闲机位关闭报修必须被拒绝")
		}
		if code := appErrorCode(t, err, rule); code != constants.CodeConflict {
			failRule(t, rule, "错误码应为 %d(CodeConflict): got=%d", constants.CodeConflict, code)
		}
		assertStationStatus(t, rule, f.db, st.ID, constants.StationIdle)
	})
}

// TestRepairRuleLegacyFaultRecovered R11：故障但没有待处理报修单的机位（历史遗留）
// 填写处理结果后可恢复空闲，并补录一条已关闭报修记录，不再卡死。
func TestRepairRuleLegacyFaultRecovered(t *testing.T) {
	const rule = "R11 历史遗留故障可凭处理结果恢复"
	f := newRepairFixture(t)
	st := f.seedStation(t, constants.StationFault) // 历史脏数据：故障但无报修单

	// 空白处理结果仍拒绝，且不落任何记录
	if _, _, _, err := f.repairSvc.Close(ruleAdmin, st.ID, "   "); err == nil {
		failRule(t, rule, "空白处理结果必须被拒绝")
	} else if appErrorCode(t, err, rule) != constants.CodeValidation {
		failRule(t, rule, "空白结果错误码应为 %d: got=%d", constants.CodeValidation, appErrorCode(t, err, rule))
	}
	var before int64
	f.db.Model(&model.RepairRecord{}).Count(&before)
	if before != 0 {
		failRule(t, rule, "校验失败不应落库: records=%d", before)
	}
	assertStationStatus(t, rule, f.db, st.ID, constants.StationFault)

	// 店员填写处理结果后恢复空闲（换店员，验证处理角色快照）
	rec, station, legacy, err := f.repairSvc.Close(ruleStaff, st.ID, "排查为主板松动，已紧固并复测正常")
	if err != nil {
		failRule(t, rule, "遗留故障恢复不应失败: %v", err)
	}
	if !legacy {
		failRule(t, rule, "无待处理单时应标记 legacy=true（补录已关闭记录）")
	}
	if station.Status != constants.StationIdle {
		failRule(t, rule, "机位应恢复 idle: got=%s", station.Status)
	}
	assertStationStatus(t, rule, f.db, st.ID, constants.StationIdle)

	// 补录记录为已关闭，无登记人（历史遗留），但完整留存处理结果/角色/时间，原因留痕
	if rec.Status != constants.RepairClosed {
		failRule(t, rule, "补录记录应为 closed: got=%s", rec.Status)
	}
	if rec.HandleResult != "排查为主板松动，已紧固并复测正常" {
		failRule(t, rule, "处理结果错误: got=%q", rec.HandleResult)
	}
	if rec.HandleUserID != ruleStaff.UserID || rec.HandleByName != ruleStaff.Username || rec.HandleRole != constants.RoleStaff {
		failRule(t, rule, "处理角色快照错误: got=(%d,%s,%s)", rec.HandleUserID, rec.HandleByName, rec.HandleRole)
	}
	if rec.HandledAt == nil || rec.HandledAt.IsZero() {
		failRule(t, rule, "处理时间不应为空")
	}
	if rec.ReportUserID != 0 || rec.ReportByName != "" || rec.ReportRole != "" {
		failRule(t, rule, "遗留补录不应有登记人快照: got=(%d,%s,%s)", rec.ReportUserID, rec.ReportByName, rec.ReportRole)
	}
	if rec.Reason != constants.MsgRepairLegacyReason {
		failRule(t, rule, "补录原因应为历史遗留占位文案: got=%q", rec.Reason)
	}
	if rec.ReportedAt.IsZero() {
		failRule(t, rule, "补录记录 reported_at 不应为零值")
	}

	// 无待处理报修、历史恰好 1 条已关闭记录
	if n := countPending(t, rule, f.db, st.ID); n != 0 {
		failRule(t, rule, "恢复后不应存在待处理报修: got=%d", n)
	}
	var closedCount int64
	f.db.Model(&model.RepairRecord{}).Where("station_id = ? AND status = ?", st.ID, constants.RepairClosed).Count(&closedCount)
	if closedCount != 1 {
		failRule(t, rule, "应补录恰好 1 条已关闭记录: got=%d", closedCount)
	}

	// 恢复后机位可正常重新登记报修（闭环可循环）
	rec2, faultStation, err := f.repairSvc.Report(ruleAdmin, st.ID, "恢复后再次故障")
	if err != nil {
		failRule(t, rule, "恢复后重新登记不应失败: %v", err)
	}
	assertPending(t, rule, rec2, st.ID, ruleAdmin, "恢复后再次故障")
	if faultStation.Status != constants.StationFault {
		failRule(t, rule, "重新登记后机位应为 fault: got=%s", faultStation.Status)
	}

	// 此时存在待处理单，再次走关闭应走正常路径（legacy=false）并成功
	rec3, idleStation, legacy3, err := f.repairSvc.Close(ruleAdmin, st.ID, "第二次修复完成")
	if err != nil {
		failRule(t, rule, "正常关闭不应失败: %v", err)
	}
	if legacy3 {
		failRule(t, rule, "存在待处理单时应走正常关闭路径，legacy 必须为 false")
	}
	if rec3.ID == rec.ID || rec3.Status != constants.RepairClosed {
		failRule(t, rule, "第二次关闭应作用于新报修单 #%d，实际记录 #%d status=%s", rec2.ID, rec3.ID, rec3.Status)
	}
	if idleStation.Status != constants.StationIdle {
		failRule(t, rule, "机位应再次恢复 idle: got=%s", idleStation.Status)
	}
}

// TestRepairRuleReportIllegalStatus R5：使用中/已预约不能登记故障。
func TestRepairRuleReportIllegalStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
	}{
		{"using 使用中", constants.StationUsing},
		{"reserved 已预约", constants.StationReserved},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := "R5 " + tc.name + "机位不能登记故障"
			f := newRepairFixture(t)
			st := f.seedStation(t, tc.status)
			_, _, err := f.repairSvc.Report(ruleAdmin, st.ID, "故障")
			if err == nil {
				failRule(t, rule, "该状态机位登记故障必须被拒绝")
			}
			if code := appErrorCode(t, err, rule); code != constants.CodeConflict {
				failRule(t, rule, "错误码应为 %d(CodeConflict): got=%d", constants.CodeConflict, code)
			}
			assertStationStatus(t, rule, f.db, st.ID, tc.status)
			if n := countPending(t, rule, f.db, st.ID); n != 0 {
				failRule(t, rule, "拒绝后不应产生报修记录: got=%d", n)
			}
		})
	}
}

// TestRepairRuleRequiredFields R6：原因与结果必填。
func TestRepairRuleRequiredFields(t *testing.T) {
	t.Run("空原因拒绝且不落库", func(t *testing.T) {
		const rule = "R6a 登记原因必填"
		f := newRepairFixture(t)
		st := f.seedStation(t, constants.StationIdle)
		for _, reason := range []string{"", "   ", "\t\n"} {
			if _, _, err := f.repairSvc.Report(ruleAdmin, st.ID, reason); err == nil {
				failRule(t, rule, "空白原因(%q)必须被拒绝", reason)
			} else if appErrorCode(t, err, rule) != constants.CodeValidation {
				failRule(t, rule, "空白原因(%q)错误码应为 %d: got=%d", reason, constants.CodeValidation, appErrorCode(t, err, rule))
			}
		}
		var n int64
		f.db.Model(&model.RepairRecord{}).Count(&n)
		if n != 0 {
			failRule(t, rule, "校验失败不应落库: records=%d", n)
		}
		assertStationStatus(t, rule, f.db, st.ID, constants.StationIdle)
	})

	t.Run("空结果拒绝且记录保持pending", func(t *testing.T) {
		const rule = "R6b 恢复处理结果必填"
		f := newRepairFixture(t)
		st := f.seedStation(t, constants.StationIdle)
		rec, _, err := f.repairSvc.Report(ruleAdmin, st.ID, "故障原因")
		if err != nil {
			failRule(t, rule, "前置登记失败: %v", err)
		}
		for _, result := range []string{"", "   "} {
			if _, _, _, err := f.repairSvc.Close(ruleAdmin, st.ID, result); err == nil {
				failRule(t, rule, "空白处理结果(%q)必须被拒绝", result)
			} else if appErrorCode(t, err, rule) != constants.CodeValidation {
				failRule(t, rule, "空白结果错误码应为 %d: got=%d", constants.CodeValidation, appErrorCode(t, err, rule))
			}
		}
		var fresh model.RepairRecord
		if err := f.db.First(&fresh, rec.ID).Error; err != nil {
			failRule(t, rule, "回读报修记录失败: %v", err)
		}
		if fresh.Status != constants.RepairPending || fresh.HandledAt != nil {
			failRule(t, rule, "校验失败后记录应保持 pending 且无处理时间: status=%s handledAt=%v", fresh.Status, fresh.HandledAt)
		}
		assertStationStatus(t, rule, f.db, st.ID, constants.StationFault)
	})
}

// TestRepairRuleDetailHistoryOverTwenty R7：连续闭环 21 次后，
// 机位详情仅返回最近 20 条（最新在前），全部 21 条可通过分页列表查到，
// 且每条记录的原因/结果/角色/时间快照正确。
func TestRepairRuleDetailHistoryOverTwenty(t *testing.T) {
	const rule = "R7 详情只展示最近20条历史"
	f := newRepairFixture(t)
	st := f.seedStation(t, constants.StationIdle)

	const cycles = 21
	reasons := make([]string, cycles)
	results := make([]string, cycles)
	for i := 0; i < cycles; i++ {
		reasons[i] = fmt.Sprintf("第%d次故障：设备编号%03d异常", i+1, i+1)
		results[i] = fmt.Sprintf("第%d次处理：更换配件并复检合格", i+1)
		// 奇数轮店员登记/管理员处理，偶数轮管理员登记/店员处理，验证两种角色快照
		reporter := ruleAdmin
		handler := ruleStaff
		if i%2 == 1 {
			reporter = ruleStaff
			handler = ruleAdmin
		}
		rec, station, err := f.repairSvc.Report(reporter, st.ID, reasons[i])
		if err != nil {
			failRule(t, rule, "第%d轮登记失败: %v", i+1, err)
		}
		assertPending(t, rule, rec, st.ID, reporter, reasons[i])
		if station.Status != constants.StationFault {
			failRule(t, rule, "第%d轮登记后机位应为 fault: got=%s", i+1, station.Status)
		}
		closed, station, legacy, err := f.repairSvc.Close(handler, st.ID, results[i])
		if err != nil {
			failRule(t, rule, "第%d轮恢复失败: %v", i+1, err)
		}
		if legacy {
			failRule(t, rule, "第%d轮为正常闭环，不应走遗留补录", i+1)
		}
		assertClosed(t, rule, closed, handler, results[i])
		if station.Status != constants.StationIdle {
			failRule(t, rule, "第%d轮恢复后机位应为 idle: got=%s", i+1, station.Status)
		}
	}

	// 最终机位空闲、无待处理报修
	detail, err := f.repairSvc.Detail(st.ID)
	if err != nil {
		failRule(t, rule, "查询详情失败: %v", err)
	}
	stationAny, ok := detail.Station.(*model.Station)
	if !ok || stationAny.Status != constants.StationIdle {
		failRule(t, rule, "详情机位应为 idle: %#v", detail.Station)
	}
	if open, _ := detail.OpenRepair.(*model.RepairRecord); open != nil {
		failRule(t, rule, "21轮闭环后不应存在待处理报修: %#v", open)
	}

	// 详情历史上限 20 条，且为最新在前（第 21 轮排第一，第 2~21 轮可见，第 1 轮被截断）
	if len(detail.RepairRecords) != 20 {
		failRule(t, rule, "详情历史应截断为最近20条: got=%d", len(detail.RepairRecords))
	}
	first, ok := detail.RepairRecords[0].(model.RepairRecord)
	if !ok {
		failRule(t, rule, "历史记录类型错误: %T", detail.RepairRecords[0])
	}
	if first.Reason != reasons[cycles-1] || first.HandleResult != results[cycles-1] {
		failRule(t, rule, "详情第一条应为第21轮记录: reason=%q result=%q", first.Reason, first.HandleResult)
	}
	last := detail.RepairRecords[19].(model.RepairRecord)
	if last.Reason != reasons[1] {
		failRule(t, rule, "详情第二十条应为第2轮记录（第1轮被截断）: got reason=%q want=%q", last.Reason, reasons[1])
	}

	// 详情中每条记录均为已关闭，且原因/结果/角色/时间齐全且顺序合法
	for i, item := range detail.RepairRecords {
		r, ok := item.(model.RepairRecord)
		if !ok {
			failRule(t, rule, "历史[%d] 类型错误: %T", i, item)
		}
		cycle := cycles - 1 - i // 第 i 条对应第几轮（1-based: cycles-i）
		wantReason := reasons[cycle]
		wantResult := results[cycle]
		if r.Status != constants.RepairClosed || r.Reason != wantReason || r.HandleResult != wantResult {
			failRule(t, rule, "历史[%d]（第%d轮）内容错误: status=%s reason=%q result=%q", i, cycle+1, r.Status, r.Reason, r.HandleResult)
		}
		if r.ReportByName == "" || r.ReportRole == "" || r.ReportedAt.IsZero() {
			failRule(t, rule, "历史[%d] 登记快照缺失: name=%s role=%s at=%v", i, r.ReportByName, r.ReportRole, r.ReportedAt)
		}
		if r.HandleByName == "" || r.HandleRole == "" || r.HandledAt == nil {
			failRule(t, rule, "历史[%d] 处理快照缺失: name=%s role=%s at=%v", i, r.HandleByName, r.HandleRole, r.HandledAt)
		}
		if r.HandledAt.Before(r.ReportedAt) {
			failRule(t, rule, "历史[%d] 处理时间早于登记时间", i)
		}
	}

	// 分页列表可查到全部 21 条
	all, total, err := f.repairSvc.List(&dto.RepairQuery{Page: 1, PageSize: 100})
	if err != nil {
		failRule(t, rule, "分页列表查询失败: %v", err)
	}
	if total != cycles || len(all) != cycles {
		failRule(t, rule, "分页列表应包含全部%d条: total=%d len=%d", cycles, total, len(all))
	}
	// 最早一轮（第1轮）虽不在详情中，但必须能在列表查到
	var oldest *model.RepairRecord
	for i := range all {
		if all[i].Reason == reasons[0] {
			oldest = &all[i]
		}
	}
	if oldest == nil {
		failRule(t, rule, "第1轮记录应可通过分页列表查到")
	}
	if oldest.HandleResult != results[0] {
		failRule(t, rule, "第1轮处理结果错误: got=%q want=%q", oldest.HandleResult, results[0])
	}
}

// TestRepairRulePlainStatusAPIBlocked R8：原状态接口不能绕过报修闭环，且原有操作保持可用。
func TestRepairRulePlainStatusAPIBlocked(t *testing.T) {
	const rule = "R8 原状态接口与报修闭环"
	f := newRepairFixture(t)
	st := f.seedStation(t, constants.StationIdle)

	// idle -> fault 不带原因：拒绝
	_, err := f.stationSvc.UpdateStatus(st.ID, &dto.UpdateStationStatusReq{Status: constants.StationFault}, "admin")
	if err == nil {
		failRule(t, rule, "旧接口 idle->fault 必须被拒绝（必须走报修登记）")
	}
	if code := appErrorCode(t, err, rule); code != constants.CodeRepairOpen {
		failRule(t, rule, "旧接口 idle->fault 错误码应为 %d: got=%d", constants.CodeRepairOpen, code)
	}
	assertStationStatus(t, rule, f.db, st.ID, constants.StationIdle)

	// 走闭环登记后，旧接口 fault -> idle 不带结果：拒绝
	if _, _, err := f.repairSvc.Report(ruleAdmin, st.ID, "鼠标失灵"); err != nil {
		failRule(t, rule, "前置登记失败: %v", err)
	}
	_, err = f.stationSvc.UpdateStatus(st.ID, &dto.UpdateStationStatusReq{Status: constants.StationIdle}, "admin")
	if err == nil {
		failRule(t, rule, "旧接口 fault->idle 必须被拒绝（必须走报修关闭）")
	}
	if code := appErrorCode(t, err, rule); code != constants.CodeRepairNone {
		failRule(t, rule, "旧接口 fault->idle 错误码应为 %d: got=%d", constants.CodeRepairNone, code)
	}
	assertStationStatus(t, rule, f.db, st.ID, constants.StationFault)

	// 原有非故障状态操作保持可用：reserved -> idle
	reserved := f.seedStation(t, constants.StationReserved)
	updated, err := f.stationSvc.UpdateStatus(reserved.ID, &dto.UpdateStationStatusReq{Status: constants.StationIdle}, "staff")
	if err != nil {
		failRule(t, rule, "原有操作 reserved->idle 应保持可用: %v", err)
	}
	if updated.Status != constants.StationIdle {
		failRule(t, rule, "reserved->idle 后状态错误: got=%s", updated.Status)
	}
}
