// Package repairtest 报修闭环端到端 HTTP 集成测试：
// 使用 httptest + 内存 sqlite 跑通真实路由（含 JWT 认证与 RBAC 中间件），
// 覆盖登记、恢复、重复登记、越权、非法状态五类验证场景。
package repairtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/esportsbar/backend/internal/constants"
	"github.com/esportsbar/backend/internal/handler"
	"github.com/esportsbar/backend/internal/middleware"
	"github.com/esportsbar/backend/internal/model"
	"github.com/esportsbar/backend/internal/repository"
	"github.com/esportsbar/backend/internal/router"
	"github.com/esportsbar/backend/internal/service"
	"github.com/esportsbar/backend/internal/util"
)

const testSecret = "integration_test_secret"

var dsnSeq int64

type env struct {
	t      *testing.T
	srv    *gin.Engine
	db     *gorm.DB
	tokens map[string]string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	n := atomic.AddInt64(&dsnSeq, 1)
	dsn := fmt.Sprintf("file:repair_it_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Station{}, &model.RepairRecord{}, &model.AuditLog{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	stations := []model.Station{
		{Name: "A区-01", Area: "A区", StationType: "seat", Status: constants.StationIdle},
		{Name: "A区-02", Area: "A区", StationType: "seat", Status: constants.StationUsing},
		{Name: "B区-01", Area: "B区", StationType: "seat", Status: constants.StationReserved},
	}
	if err := db.Create(&stations).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	logger := util.NewLogger("error")
	stationRepo := repository.NewStationRepository(db)
	repairRepo := repository.NewRepairRepository(db)
	auditRepo := repository.NewAuditRepository(db)
	stationSvc := service.NewStationService(stationRepo, repairRepo, logger)
	repairSvc := service.NewRepairService(repairRepo, stationRepo, db, logger)
	auditSvc := service.NewAuditService(auditRepo, logger)
	stationH := handler.NewStationHandler(stationSvc, logger)
	repairH := handler.NewRepairHandler(repairSvc, logger)

	gin.SetMode(gin.TestMode)
	srv := gin.New()
	api := srv.Group("/api/v1")
	api.Use(middleware.Audit(auditSvc))
	router.RegisterStation(api, stationH, testSecret)
	router.RegisterRepair(api, repairH, testSecret)

	tok := func(uid uint, name, role string) string {
		tk, err := util.GenerateToken(testSecret, 3600, uid, name, role)
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		return tk
	}
	return &env{
		t: t, srv: srv, db: db,
		tokens: map[string]string{
			"admin":  tok(1, "admin", constants.RoleAdmin),
			"staff":  tok(2, "clerk", constants.RoleStaff),
			"member": tok(3, "gamer", constants.RoleMember),
		},
	}
}

type apiBody struct {
	Code int             `json:"code"`
	Msg  string          `json:"message"`
	Data json.RawMessage `json:"data"`
}

func (e *env) do(role, method, path string, payload any) (int, apiBody) {
	e.t.Helper()
	var body []byte
	if payload != nil {
		body, _ = json.Marshal(payload)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if role != "" {
		req.Header.Set("Authorization", "Bearer "+e.tokens[role])
	}
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, req)
	var b apiBody
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	return w.Code, b
}

func mustCode(t *testing.T, b apiBody, want int, scene string) {
	t.Helper()
	if b.Code != want {
		t.Fatalf("[%s] code=%d msg=%s, want %d", scene, b.Code, b.Msg, want)
	}
}

// TestITReportRecoverLifecycle 店员登记 -> 重复登记拒绝 -> 空结果拒绝 -> 恢复关闭 -> 二次登记成功。
func TestITReportRecoverLifecycle(t *testing.T) {
	e := newEnv(t)

	// 未登录：401
	if st, b := e.do("", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{"reason": "x"}); st != http.StatusUnauthorized || b.Code != constants.CodeUnauthorized {
		t.Fatalf("anonymous report should be 401, got %d/%d", st, b.Code)
	}

	// 会员越权登记：403
	st, b := e.do("member", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{"reason": "耳机没声"})
	if st != http.StatusForbidden || b.Code != constants.CodeForbidden {
		t.Fatalf("member report should be 403, got %d/%d %s", st, b.Code, b.Msg)
	}

	// 店员正常登记
	st, b = e.do("staff", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{"reason": "键盘失灵"})
	if st != http.StatusOK || b.Code != constants.CodeOK {
		t.Fatalf("staff report should succeed, got %d/%d %s", st, b.Code, b.Msg)
	}

	// 机位状态已是 fault，管理员重复登记：409 CodeRepairOpen
	st, b = e.do("admin", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{"reason": "再报一次"})
	if st != http.StatusConflict || b.Code != constants.CodeRepairOpen {
		t.Fatalf("duplicate report should be 409/%d, got %d/%d %s", constants.CodeRepairOpen, st, b.Code, b.Msg)
	}

	// 详情可查待处理报修及登记角色/时间
	_, b = e.do("staff", http.MethodGet, "/api/v1/stations/1/detail", nil)
	mustCode(t, b, constants.CodeOK, "detail")
	var detail struct {
		Station struct {
			Status string `json:"status"`
		} `json:"station"`
		OpenRepair *struct {
			Reason       string `json:"reason"`
			ReportByName string `json:"report_by_name"`
			ReportRole   string `json:"report_role"`
			ReportedAt   string `json:"reported_at"`
		} `json:"open_repair"`
		RepairRecords []map[string]any `json:"repair_records"`
	}
	_ = json.Unmarshal(b.Data, &detail)
	if detail.Station.Status != constants.StationFault || detail.OpenRepair == nil {
		t.Fatalf("detail should show fault + open repair: %s", b.Data)
	}
	if detail.OpenRepair.Reason != "键盘失灵" || detail.OpenRepair.ReportByName != "clerk" || detail.OpenRepair.ReportRole != constants.RoleStaff || detail.OpenRepair.ReportedAt == "" {
		t.Fatalf("open repair snapshot wrong: %+v", detail.OpenRepair)
	}

	// 会员越权恢复：403
	if st, b = e.do("member", http.MethodPost, "/api/v1/stations/1/repair-close", map[string]string{"handle_result": "好了"}); st != http.StatusForbidden {
		t.Fatalf("member close should be 403, got %d", st)
	}

	// 空处理结果：400 校验失败
	if st, b = e.do("admin", http.MethodPost, "/api/v1/stations/1/repair-close", map[string]string{"handle_result": "   "}); st != http.StatusBadRequest || b.Code != constants.CodeValidation {
		t.Fatalf("empty result should be validation error, got %d/%d %s", st, b.Code, b.Msg)
	}

	// 管理员恢复关闭
	st, b = e.do("admin", http.MethodPost, "/api/v1/stations/1/repair-close", map[string]string{"handle_result": "更换键盘，测试正常"})
	if st != http.StatusOK || b.Code != constants.CodeOK {
		t.Fatalf("close should succeed, got %d/%d %s", st, b.Code, b.Msg)
	}
	var closed struct {
		Repair struct {
			Status       string  `json:"status"`
			HandleResult string  `json:"handle_result"`
			HandleByName string  `json:"handle_by_name"`
			HandleRole   string  `json:"handle_role"`
			HandledAt    *string `json:"handled_at"`
		} `json:"repair"`
		Station struct {
			Status string `json:"status"`
		} `json:"station"`
	}
	_ = json.Unmarshal(b.Data, &closed)
	if closed.Repair.Status != constants.RepairClosed || closed.Station.Status != constants.StationIdle ||
		closed.Repair.HandleByName != "admin" || closed.Repair.HandleRole != constants.RoleAdmin ||
		closed.Repair.HandleResult != "更换键盘，测试正常" || closed.Repair.HandledAt == nil {
		t.Fatalf("close payload wrong: %+v", closed)
	}

	// 机位已恢复空闲，再次恢复属于非法状态：409 CodeConflict
	st, b = e.do("staff", http.MethodPost, "/api/v1/stations/1/repair-close", map[string]string{"handle_result": "x"})
	if st != http.StatusConflict || b.Code != constants.CodeConflict {
		t.Fatalf("re-close idle station should be 409/%d, got %d/%d", constants.CodeConflict, st, b.Code)
	}

	// 关闭后可以重新登记（闭环可循环）
	if st, b = e.do("staff", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{"reason": "第二次故障"}); st != http.StatusOK {
		t.Fatalf("re-report after close should succeed, got %d/%d %s", st, b.Code, b.Msg)
	}

	// 报修列表（店员）按机位与状态筛选
	_, b = e.do("staff", http.MethodGet, "/api/v1/stations/1/repairs", nil)
	mustCode(t, b, constants.CodeOK, "list by station")
	var records []map[string]any
	_ = json.Unmarshal(b.Data, &records)
	if len(records) != 2 {
		t.Fatalf("station repair history = %d, want 2", len(records))
	}

	_, b = e.do("admin", http.MethodGet, "/api/v1/repairs?status=pending&page=1&page_size=10", nil)
	mustCode(t, b, constants.CodeOK, "list pending")
	var page struct {
		List  []map[string]any `json:"list"`
		Total int64            `json:"total"`
	}
	_ = json.Unmarshal(b.Data, &page)
	if page.Total != 1 {
		t.Fatalf("pending repairs total = %d, want 1", page.Total)
	}

	// 会员访问报修管理列表：403
	if st, _ = e.do("member", http.MethodGet, "/api/v1/repairs", nil); st != http.StatusForbidden {
		t.Fatalf("member list repairs should be 403, got %d", st)
	}
}

// TestITReportIllegalStationStatus 使用中/预约机位不能登记故障。
func TestITReportIllegalStationStatus(t *testing.T) {
	e := newEnv(t)
	// station 2 = using
	st, b := e.do("staff", http.MethodPost, "/api/v1/stations/2/repair-report", map[string]string{"reason": "故障"})
	if st != http.StatusConflict || b.Code != constants.CodeConflict {
		t.Fatalf("report on using station should be 409, got %d/%d %s", st, b.Code, b.Msg)
	}
	// station 3 = reserved
	st, b = e.do("admin", http.MethodPost, "/api/v1/stations/3/repair-report", map[string]string{"reason": "故障"})
	if st != http.StatusConflict || b.Code != constants.CodeConflict {
		t.Fatalf("report on reserved station should be 409, got %d/%d %s", st, b.Code, b.Msg)
	}
	// 不存在的机位：404
	st, b = e.do("admin", http.MethodPost, "/api/v1/stations/999/repair-report", map[string]string{"reason": "故障"})
	if st != http.StatusNotFound || b.Code != constants.CodeNotFound {
		t.Fatalf("report on missing station should be 404, got %d/%d", st, b.Code)
	}
	// 机位不存在时恢复：404
	st, _ = e.do("admin", http.MethodPost, "/api/v1/stations/999/repair-close", map[string]string{"handle_result": "x"})
	if st != http.StatusNotFound {
		t.Fatalf("close missing station should be 404, got %d", st)
	}
}

// TestITPlainStatusAPICannotBypass 原状态接口不能绕过报修闭环：
// idle->fault 与 fault->idle 均被拒绝；原有 reserved->idle 操作保持可用。
func TestITPlainStatusAPICannotBypass(t *testing.T) {
	e := newEnv(t)

	// 旧接口直接标记故障（不带原因）：409 CodeRepairOpen
	st, b := e.do("staff", http.MethodPut, "/api/v1/stations/1/status", map[string]string{"status": "fault"})
	if st != http.StatusConflict || b.Code != constants.CodeRepairOpen {
		t.Fatalf("plain idle->fault should be 409/%d, got %d/%d %s", constants.CodeRepairOpen, st, b.Code, b.Msg)
	}

	// 先通过报修闭环把机位置为故障
	st, _ = e.do("staff", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{"reason": "鼠标失灵"})
	if st != http.StatusOK {
		t.Fatalf("setup report failed: %d", st)
	}

	// 旧接口直接恢复空闲（不带处理结果）：409 CodeRepairNone
	st, b = e.do("staff", http.MethodPut, "/api/v1/stations/1/status", map[string]string{"status": "idle"})
	if st != http.StatusConflict || b.Code != constants.CodeRepairNone {
		t.Fatalf("plain fault->idle should be 409/%d, got %d/%d %s", constants.CodeRepairNone, st, b.Code, b.Msg)
	}

	// 会员调用旧状态接口：403（原有管理操作权限不变）
	if st, _ = e.do("member", http.MethodPut, "/api/v1/stations/1/status", map[string]string{"status": "idle"}); st != http.StatusForbidden {
		t.Fatalf("member status update should be 403, got %d", st)
	}

	// 原有非故障状态操作保持可用：reserved(station 3) -> idle
	st, b = e.do("admin", http.MethodPut, "/api/v1/stations/3/status", map[string]string{"status": "idle"})
	if st != http.StatusOK || b.Code != constants.CodeOK {
		t.Fatalf("reserved->idle via original API should still work, got %d/%d %s", st, b.Code, b.Msg)
	}

	// 非法状态流转：idle(station 1 当前 fault) -> reserved 被状态机拒绝
	st, b = e.do("admin", http.MethodPut, "/api/v1/stations/1/status", map[string]string{"status": "reserved"})
	if st != http.StatusConflict || b.Code != constants.CodeConflict {
		t.Fatalf("fault->reserved should be 409 conflict, got %d/%d", st, b.Code)
	}

	// 非法枚举值：400 校验失败
	st, b = e.do("admin", http.MethodPut, "/api/v1/stations/3/status", map[string]string{"status": "broken"})
	if st != http.StatusBadRequest || b.Code != constants.CodeValidation {
		t.Fatalf("invalid enum should be 400/validation, got %d/%d", st, b.Code)
	}
}

// TestITInvalidPayloads 参数校验。
func TestITInvalidPayloads(t *testing.T) {
	e := newEnv(t)

	// 缺少 reason
	if st, b := e.do("admin", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{}); st != http.StatusBadRequest || b.Code != constants.CodeValidation {
		t.Fatalf("missing reason should be 400, got %d/%d", st, b.Code)
	}
	// 缺少 handle_result
	if st, b := e.do("admin", http.MethodPost, "/api/v1/stations/1/repair-close", map[string]string{}); st != http.StatusBadRequest || b.Code != constants.CodeValidation {
		t.Fatalf("missing handle_result should be 400, got %d/%d", st, b.Code)
	}
	// 无效 JWT
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stations/1/repair-report", bytes.NewReader([]byte(`{"reason":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer not-a-token")
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bad token should be 401, got %d", w.Code)
	}
}
