package service

import (
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/esportsbar/backend/internal/constants"
	"github.com/esportsbar/backend/internal/dto"
	"github.com/esportsbar/backend/internal/model"
	"github.com/esportsbar/backend/internal/repository"
	"github.com/esportsbar/backend/internal/util"
)

// newRepairTestDB 使用纯 Go sqlite 驱动构造内存数据库（仅测试，跳过 MySQL 专属生成列索引）。
func newRepairTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Station{}, &model.RepairRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 每个测试使用独立内存库：清空残留。
	if err := db.Exec("DELETE FROM repair_records").Error; err != nil {
		t.Fatalf("cleanup repairs: %v", err)
	}
	if err := db.Exec("DELETE FROM stations").Error; err != nil {
		t.Fatalf("cleanup stations: %v", err)
	}
	return db
}

func newRepairService(db *gorm.DB) (*RepairService, *StationService) {
	logger := newTestLogger()
	stationRepo := repository.NewStationRepository(db)
	repairRepo := repository.NewRepairRepository(db)
	return NewRepairService(repairRepo, stationRepo, db, logger), NewStationService(stationRepo, repairRepo, logger)
}

func seedStation(t *testing.T, db *gorm.DB, status string) *model.Station {
	t.Helper()
	st := &model.Station{Name: "A区-01", Area: "A区", StationType: "seat", Status: status}
	if err := db.Create(st).Error; err != nil {
		t.Fatalf("seed station: %v", err)
	}
	return st
}

var adminOp = Operator{UserID: 1, Username: "admin", Role: constants.RoleAdmin}
var staffOp = Operator{UserID: 2, Username: "clerk", Role: constants.RoleStaff}

func appErrorCode(t *testing.T, err error) int {
	t.Helper()
	var appErr *util.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected AppError, got %T: %v", err, err)
	}
	return appErr.Code
}

// TestRepairReportAndClose 登记报修 -> 机位故障 -> 恢复关闭 -> 机位空闲 的完整闭环。
func TestRepairReportAndClose(t *testing.T) {
	db := newRepairTestDB(t)
	repairSvc, _ := newRepairService(db)
	st := seedStation(t, db, constants.StationIdle)

	// 1. 登记报修
	rec, station, err := repairSvc.Report(adminOp, st.ID, "键盘失灵，鼠标断连")
	if err != nil {
		t.Fatalf("Report error: %v", err)
	}
	if rec.Status != constants.RepairPending || rec.Reason != "键盘失灵，鼠标断连" {
		t.Fatalf("unexpected repair: %+v", rec)
	}
	if rec.ReportByName != "admin" || rec.ReportRole != constants.RoleAdmin {
		t.Fatalf("report operator snapshot wrong: %+v", rec)
	}
	if station.Status != constants.StationFault {
		t.Fatalf("station should be fault, got %s", station.Status)
	}
	if rec.ReportedAt.IsZero() {
		t.Fatal("reported_at should be set")
	}

	// 2. 待处理报修可被查询
	detail, err := repairSvc.Detail(st.ID)
	if err != nil {
		t.Fatalf("Detail error: %v", err)
	}
	openDetail, ok := detail.OpenRepair.(*model.RepairRecord)
	if !ok || openDetail == nil || openDetail.ID != rec.ID {
		t.Fatalf("open repair missing: %+v", detail.OpenRepair)
	}
	if len(detail.RepairRecords) != 1 {
		t.Fatalf("repair history len = %d", len(detail.RepairRecords))
	}

	// 3. 不填处理结果不能关闭
	if _, _, err := repairSvc.Close(staffOp, st.ID, "  "); err == nil {
		t.Fatal("empty handle result should be rejected")
	} else if appErrorCode(t, err) != constants.CodeValidation {
		t.Fatalf("expect validation code, got %d", appErrorCode(t, err))
	}

	// 4. 恢复空闲并关闭报修
	closed, station2, err := repairSvc.Close(staffOp, st.ID, "已更换键鼠并测试通过")
	if err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if closed.Status != constants.RepairClosed || closed.HandleResult != "已更换键鼠并测试通过" {
		t.Fatalf("unexpected closed repair: %+v", closed)
	}
	if closed.HandleByName != "clerk" || closed.HandleRole != constants.RoleStaff || closed.HandledAt == nil {
		t.Fatalf("handle operator snapshot wrong: %+v", closed)
	}
	if station2.Status != constants.StationIdle {
		t.Fatalf("station should be idle, got %s", station2.Status)
	}

	// 5. 关闭后无待处理报修，历史保留
	hasOpen, err := repairSvc.HasOpenRepair(st.ID)
	if err != nil || hasOpen {
		t.Fatalf("HasOpenRepair = %v, %v", hasOpen, err)
	}
	records, err := repairSvc.ListByStation(st.ID)
	if err != nil || len(records) != 1 || records[0].Status != constants.RepairClosed {
		t.Fatalf("history wrong: len=%d err=%v", len(records), err)
	}
}

// TestRepairDuplicateRejected 同一机位只允许一条待处理报修。
func TestRepairDuplicateRejected(t *testing.T) {
	db := newRepairTestDB(t)
	repairSvc, _ := newRepairService(db)
	st := seedStation(t, db, constants.StationIdle)

	if _, _, err := repairSvc.Report(adminOp, st.ID, "显示器花屏"); err != nil {
		t.Fatalf("first Report error: %v", err)
	}
	// 机位已是 fault，再次登记必须被拒绝（并发/重复登记）
	_, _, err := repairSvc.Report(staffOp, st.ID, "重复报修")
	if err == nil {
		t.Fatal("duplicate report should be rejected")
	}
	if code := appErrorCode(t, err); code != constants.CodeRepairOpen {
		t.Fatalf("expect CodeRepairOpen %d, got %d", constants.CodeRepairOpen, code)
	}
	var cnt int64
	db.Model(&model.RepairRecord{}).Where("station_id = ? AND status = ?", st.ID, constants.RepairPending).Count(&cnt)
	if cnt != 1 {
		t.Fatalf("pending repair count = %d, want 1", cnt)
	}
}

// TestRepairCloseWithoutOpen 故障机位若无待处理报修（历史脏数据），不允许关闭。
func TestRepairCloseWithoutOpen(t *testing.T) {
	db := newRepairTestDB(t)
	repairSvc, _ := newRepairService(db)
	st := seedStation(t, db, constants.StationFault)

	_, _, err := repairSvc.Close(adminOp, st.ID, "修好了")
	if err == nil {
		t.Fatal("close without open repair should be rejected")
	}
	if code := appErrorCode(t, err); code != constants.CodeRepairNone {
		t.Fatalf("expect CodeRepairNone %d, got %d", constants.CodeRepairNone, code)
	}
}

// TestRepairReportOnIllegalStatus 非法机位状态（使用中/预约中）不能标记故障。
func TestRepairReportOnIllegalStatus(t *testing.T) {
	db := newRepairTestDB(t)
	repairSvc, _ := newRepairService(db)

	for _, status := range []string{constants.StationUsing, constants.StationReserved} {
		st := seedStation(t, db, status)
		_, _, err := repairSvc.Report(adminOp, st.ID, "故障")
		if err == nil {
			t.Fatalf("report on %s should be rejected", status)
		}
		if code := appErrorCode(t, err); code != constants.CodeConflict {
			t.Fatalf("status %s expect CodeConflict, got %d", status, code)
		}
	}
}

// TestRepairEmptyReason 原因必填校验。
func TestRepairEmptyReason(t *testing.T) {
	db := newRepairTestDB(t)
	repairSvc, _ := newRepairService(db)
	st := seedStation(t, db, constants.StationIdle)

	if _, _, err := repairSvc.Report(adminOp, st.ID, "  "); err == nil {
		t.Fatal("empty reason should be rejected")
	} else if appErrorCode(t, err) != constants.CodeValidation {
		t.Fatalf("expect CodeValidation, got %d", appErrorCode(t, err))
	}
	var cnt int64
	db.Model(&model.RepairRecord{}).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("no repair should be created, cnt=%d", cnt)
	}
}

// TestRepairCloseReopensForNextCycle 关闭后可重新登记（唯一约束随状态释放）。
func TestRepairCloseReopensForNextCycle(t *testing.T) {
	db := newRepairTestDB(t)
	repairSvc, _ := newRepairService(db)
	st := seedStation(t, db, constants.StationIdle)

	if _, _, err := repairSvc.Report(adminOp, st.ID, "第一次故障"); err != nil {
		t.Fatalf("report1: %v", err)
	}
	if _, _, err := repairSvc.Close(adminOp, st.ID, "第一次修复"); err != nil {
		t.Fatalf("close1: %v", err)
	}
	rec2, station, err := repairSvc.Report(staffOp, st.ID, "第二次故障")
	if err != nil {
		t.Fatalf("report2 after close should succeed: %v", err)
	}
	if station.Status != constants.StationFault {
		t.Fatalf("station should be fault again, got %s", station.Status)
	}
	records, _ := repairSvc.ListByStation(st.ID)
	if len(records) != 2 {
		t.Fatalf("history len = %d, want 2", len(records))
	}
	if records[0].ID != rec2.ID || records[0].Status != constants.RepairPending {
		t.Fatalf("newest record should be pending rec2: %+v", records[0])
	}
}

// TestStationStatusFaultBypassed 原状态流转接口不得绕过报修闭环。
func TestStationStatusFaultBypassed(t *testing.T) {
	db := newRepairTestDB(t)
	_, stationSvc := newRepairService(db)
	st := seedStation(t, db, constants.StationIdle)

	// idle -> fault 不带原因：拒绝（必须走报修登记）
	if _, err := stationSvc.UpdateStatus(st.ID, &dto.UpdateStationStatusReq{Status: constants.StationFault}, "admin"); err == nil {
		t.Fatal("idle->fault via plain status API should be rejected")
	} else if appErrorCode(t, err) != constants.CodeRepairOpen {
		t.Fatalf("expect CodeRepairOpen, got %d", appErrorCode(t, err))
	}

	// 非法状态流转 using -> fault 经由状态机拒绝（这里构造 using 机位）
	st2 := seedStation(t, db, constants.StationUsing)
	if _, err := stationSvc.UpdateStatus(st2.ID, &dto.UpdateStationStatusReq{Status: constants.StationReserved}, "admin"); err == nil {
		t.Fatal("using->reserved should be rejected by state machine")
	} else if appErrorCode(t, err) != constants.CodeConflict {
		t.Fatalf("expect CodeConflict, got %d", appErrorCode(t, err))
	}

	// 原有非故障操作保持可用：reserved -> idle
	st3 := seedStation(t, db, constants.StationReserved)
	updated, err := stationSvc.UpdateStatus(st3.ID, &dto.UpdateStationStatusReq{Status: constants.StationIdle}, "staff")
	if err != nil {
		t.Fatalf("reserved->idle should work: %v", err)
	}
	if updated.Status != constants.StationIdle {
		t.Fatalf("status = %s", updated.Status)
	}
}
