// Package repairtest 机位报修闭环 HTTP 集成测试（可重复）。
//
// 本文件覆盖需要经过真实路由栈的规则（JWT 认证中间件 + RBAC 权限中间件 + 参数绑定校验）：
//
//	R3 HTTP 重复登记返回 409/code=40010
//	R4 HTTP 无待处理记录关闭返回 409/code=40011；非故障机位关闭返回 409
//	R5 HTTP 使用中/已预约机位登记返回 409；机位不存在返回 404
//	R9 未登录（401）、会员越权（403）：登记/关闭/报修管理列表
//	R10 非法状态枚举被参数校验拒绝（400/code=42200）；旧状态接口同样拒绝绕过
//
// service 层业务规则（R1/R2/R6/R7/R8）见 internal/service/repair_service_test.go。
// 每个用例使用独立 DSN 的内存 sqlite 库，可连续反复运行。
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

type httpEnv struct {
	t      *testing.T
	srv    *gin.Engine
	db     *gorm.DB
	tokens map[string]string
}

func newHTTPEnv(t *testing.T) *httpEnv {
	t.Helper()
	n := atomic.AddInt64(&dsnSeq, 1)
	dsn := fmt.Sprintf("file:repair_http_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("[测试基建] 打开内存库失败: %v", err)
	}
	if err := db.AutoMigrate(&model.Station{}, &model.RepairRecord{}, &model.AuditLog{}); err != nil {
		t.Fatalf("[测试基建] 建表失败: %v", err)
	}
	stations := []model.Station{
		{Name: "A区-01", Area: "A区", StationType: "seat", Status: constants.StationIdle},
		{Name: "A区-02", Area: "A区", StationType: "seat", Status: constants.StationUsing},
		{Name: "B区-01", Area: "B区", StationType: "seat", Status: constants.StationReserved},
		{Name: "B区-02", Area: "B区", StationType: "seat", Status: constants.StationFault}, // 故障但无报修单（R4a）
	}
	if err := db.Create(&stations).Error; err != nil {
		t.Fatalf("[测试基建] 造机位失败: %v", err)
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
			t.Fatalf("[测试基建] 签发 JWT 失败: %v", err)
		}
		return tk
	}
	return &httpEnv{
		t: t, srv: srv, db: db,
		tokens: map[string]string{
			"admin":  tok(101, "admin", constants.RoleAdmin),
			"staff":  tok(202, "clerk", constants.RoleStaff),
			"member": tok(303, "gamer", constants.RoleMember),
		},
	}
}

type respBody struct {
	Code int             `json:"code"`
	Msg  string          `json:"message"`
	Data json.RawMessage `json:"data"`
}

func (e *httpEnv) do(role, method, path string, payload any) (int, respBody) {
	e.t.Helper()
	var reader *bytes.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if role != "" {
		req.Header.Set("Authorization", "Bearer "+e.tokens[role])
	}
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, req)
	var b respBody
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	return w.Code, b
}

func failRule(t *testing.T, rule string, format string, args ...any) {
	t.Helper()
	t.Fatalf("[%s] %s", rule, fmt.Sprintf(format, args...))
}

// TestHTTPRuleAuthAndRBAC R9：未登录 401、会员越权 403。
func TestHTTPRuleAuthAndRBAC(t *testing.T) {
	e := newHTTPEnv(t)

	t.Run("未登录登记被401拒绝", func(t *testing.T) {
		const rule = "R9a 未登录不能登记报修"
		st, b := e.do("", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{"reason": "x"})
		if st != http.StatusUnauthorized || b.Code != constants.CodeUnauthorized {
			failRule(t, rule, "期望 401/%d，实际 %d/%d %s", constants.CodeUnauthorized, st, b.Code, b.Msg)
		}
		var n int64
		e.db.Model(&model.RepairRecord{}).Count(&n)
		if n != 0 {
			failRule(t, rule, "被拒后不应落库: records=%d", n)
		}
	})

	t.Run("无效Token被401拒绝", func(t *testing.T) {
		const rule = "R9b 无效 JWT 不能登记"
		req := httptest.NewRequest(http.MethodPost, "/api/v1/stations/1/repair-report", bytes.NewReader([]byte(`{"reason":"x"}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer not-a-valid-token")
		w := httptest.NewRecorder()
		e.srv.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			failRule(t, rule, "期望 401，实际 %d", w.Code)
		}
	})

	t.Run("会员登记被403拒绝", func(t *testing.T) {
		const rule = "R9c 会员不能登记报修"
		st, b := e.do("member", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{"reason": "耳机没声"})
		if st != http.StatusForbidden || b.Code != constants.CodeForbidden {
			failRule(t, rule, "期望 403/%d，实际 %d/%d %s", constants.CodeForbidden, st, b.Code, b.Msg)
		}
	})

	t.Run("会员恢复被403拒绝", func(t *testing.T) {
		const rule = "R9d 会员不能关闭报修"
		st, b := e.do("member", http.MethodPost, "/api/v1/stations/4/repair-close", map[string]string{"handle_result": "好了"})
		if st != http.StatusForbidden || b.Code != constants.CodeForbidden {
			failRule(t, rule, "期望 403/%d，实际 %d/%d %s", constants.CodeForbidden, st, b.Code, b.Msg)
		}
	})

	t.Run("会员访问报修管理列表被403拒绝", func(t *testing.T) {
		const rule = "R9e 会员不能查看报修管理列表"
		st, _ := e.do("member", http.MethodGet, "/api/v1/repairs", nil)
		if st != http.StatusForbidden {
			failRule(t, rule, "期望 403，实际 %d", st)
		}
	})

	t.Run("会员访问旧状态接口被403拒绝", func(t *testing.T) {
		const rule = "R9f 会员不能流转机位状态"
		st, _ := e.do("member", http.MethodPut, "/api/v1/stations/1/status", map[string]string{"status": "fault"})
		if st != http.StatusForbidden {
			failRule(t, rule, "期望 403，实际 %d", st)
		}
	})

	t.Run("登录会员可查看机位详情与机位报修历史", func(t *testing.T) {
		const rule = "R9g 会员对详情/历史只读可见"
		if st, b := e.do("member", http.MethodGet, "/api/v1/stations/1/detail", nil); st != http.StatusOK || b.Code != constants.CodeOK {
			failRule(t, rule, "会员查看详情应放行: %d/%d", st, b.Code)
		}
		if st, b := e.do("member", http.MethodGet, "/api/v1/stations/1/repairs", nil); st != http.StatusOK || b.Code != constants.CodeOK {
			failRule(t, rule, "会员查看历史应放行: %d/%d", st, b.Code)
		}
	})
}

// TestHTTPRuleReportFlow R1/R2/R3 HTTP 全链路：登记、重复登记拒绝、空结果拒绝、恢复、二次闭环。
func TestHTTPRuleReportFlow(t *testing.T) {
	e := newHTTPEnv(t)

	// 缺少 reason：400
	t.Run("缺少原因参数被400拒绝", func(t *testing.T) {
		const rule = "R6 HTTP 原因必填"
		st, b := e.do("staff", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{})
		if st != http.StatusBadRequest || b.Code != constants.CodeValidation {
			failRule(t, rule, "期望 400/%d，实际 %d/%d %s", constants.CodeValidation, st, b.Code, b.Msg)
		}
	})

	// 店员登记成功，断言机位状态/报修状态/原因/角色/时间
	st, b := e.do("staff", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{"reason": "键盘失灵"})
	if st != http.StatusOK || b.Code != constants.CodeOK {
		t.Fatalf("[R1 HTTP] 店员登记失败: %d/%d %s", st, b.Code, b.Msg)
	}
	var report struct {
		Repair  model.RepairRecord `json:"repair"`
		Station model.Station      `json:"station"`
	}
	_ = json.Unmarshal(b.Data, &report)
	if report.Station.Status != constants.StationFault {
		t.Fatalf("[R1 HTTP] 机位应为 fault: %s", report.Station.Status)
	}
	if report.Repair.Status != constants.RepairPending || report.Repair.Reason != "键盘失灵" ||
		report.Repair.ReportByName != "clerk" || report.Repair.ReportRole != constants.RoleStaff ||
		report.Repair.ReportedAt.IsZero() {
		t.Fatalf("[R1 HTTP] 报修快照错误: %+v", report.Repair)
	}

	// R3 管理员重复登记：409/40010
	t.Run("重复登记被409拒绝", func(t *testing.T) {
		const rule = "R3 HTTP 同机位重复登记"
		st, b := e.do("admin", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{"reason": "再报一次"})
		if st != http.StatusConflict || b.Code != constants.CodeRepairOpen {
			failRule(t, rule, "期望 409/%d，实际 %d/%d %s", constants.CodeRepairOpen, st, b.Code, b.Msg)
		}
		var n int64
		e.db.Model(&model.RepairRecord{}).Where("station_id = ?", 1).Count(&n)
		if n != 1 {
			failRule(t, rule, "拒绝重复登记后记录应仍为 1 条: got=%d", n)
		}
	})

	// 详情中能看到待处理报修及登记快照
	t.Run("详情展示待处理报修快照", func(t *testing.T) {
		const rule = "R1 HTTP 详情展示原因/角色/时间"
		_, b := e.do("staff", http.MethodGet, "/api/v1/stations/1/detail", nil)
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
		}
		_ = json.Unmarshal(b.Data, &detail)
		if detail.Station.Status != constants.StationFault || detail.OpenRepair == nil {
			failRule(t, rule, "详情应为 fault 且带 open_repair: %s", b.Data)
		}
		if detail.OpenRepair.Reason != "键盘失灵" || detail.OpenRepair.ReportByName != "clerk" ||
			detail.OpenRepair.ReportRole != constants.RoleStaff || detail.OpenRepair.ReportedAt == "" {
			failRule(t, rule, "待处理报修快照错误: %+v", detail.OpenRepair)
		}
	})

	// 空白处理结果：binding 对纯空格放行，由 service 层 trim 后以 CodeValidation 拒绝
	t.Run("空白处理结果被拒绝", func(t *testing.T) {
		const rule = "R6 HTTP 结果必填"
		_, b := e.do("admin", http.MethodPost, "/api/v1/stations/1/repair-close", map[string]string{"handle_result": "   "})
		if b.Code != constants.CodeValidation {
			failRule(t, rule, "空白结果业务码应为 %d，实际 %d %s", constants.CodeValidation, b.Code, b.Msg)
		}
	})

	// 管理员恢复，断言机位 idle/报修 closed/结果/处理角色/时间
	st, b = e.do("admin", http.MethodPost, "/api/v1/stations/1/repair-close", map[string]string{"handle_result": "更换键盘，测试正常"})
	if st != http.StatusOK || b.Code != constants.CodeOK {
		t.Fatalf("[R2 HTTP] 恢复失败: %d/%d %s", st, b.Code, b.Msg)
	}
	var closed struct {
		Repair  model.RepairRecord `json:"repair"`
		Station model.Station      `json:"station"`
	}
	_ = json.Unmarshal(b.Data, &closed)
	if closed.Station.Status != constants.StationIdle {
		t.Fatalf("[R2 HTTP] 机位应为 idle: %s", closed.Station.Status)
	}
	if closed.Repair.Status != constants.RepairClosed || closed.Repair.HandleResult != "更换键盘，测试正常" ||
		closed.Repair.HandleByName != "admin" || closed.Repair.HandleRole != constants.RoleAdmin ||
		closed.Repair.HandledAt == nil {
		t.Fatalf("[R2 HTTP] 关闭快照错误: %+v", closed.Repair)
	}

	// R4 恢复后再次关闭（机位已 idle）：409/CodeConflict
	t.Run("空闲机位再次关闭被409拒绝", func(t *testing.T) {
		const rule = "R4 HTTP 非故障机位不能关闭"
		st, b := e.do("staff", http.MethodPost, "/api/v1/stations/1/repair-close", map[string]string{"handle_result": "x"})
		if st != http.StatusConflict || b.Code != constants.CodeConflict {
			failRule(t, rule, "期望 409/%d，实际 %d/%d %s", constants.CodeConflict, st, b.Code, b.Msg)
		}
	})

	// 闭环后可再次登记，历史累计 2 条且含完整结果快照
	t.Run("关闭后可二次登记且历史正确", func(t *testing.T) {
		const rule = "R2/R7 HTTP 闭环可循环"
		if st, b := e.do("staff", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{"reason": "第二次故障"}); st != http.StatusOK {
			failRule(t, rule, "二次登记应成功: %d/%d %s", st, b.Code, b.Msg)
		}
		if st, b := e.do("staff", http.MethodPost, "/api/v1/stations/1/repair-close", map[string]string{"handle_result": "第二次修复"}); st != http.StatusOK {
			failRule(t, rule, "二次恢复应成功: %d/%d %s", st, b.Code, b.Msg)
		}
		_, b := e.do("admin", http.MethodGet, "/api/v1/stations/1/repairs", nil)
		var records []model.RepairRecord
		_ = json.Unmarshal(b.Data, &records)
		if len(records) != 2 {
			failRule(t, rule, "历史应为 2 条: got=%d", len(records))
		}
		// 最新在前
		if records[0].HandleResult != "第二次修复" || records[1].HandleResult != "更换键盘，测试正常" {
			failRule(t, rule, "历史排序或结果错误: %q / %q", records[0].HandleResult, records[1].HandleResult)
		}
	})
}

// TestHTTPRuleIllegalStatus R5：使用中/已预约不能登记；不存在机位 404；故障无报修单不能关闭。
func TestHTTPRuleIllegalStatus(t *testing.T) {
	e := newHTTPEnv(t)

	t.Run("使用中机位登记被409拒绝", func(t *testing.T) {
		const rule = "R5 HTTP 使用中不能登记"
		st, b := e.do("staff", http.MethodPost, "/api/v1/stations/2/repair-report", map[string]string{"reason": "故障"})
		if st != http.StatusConflict || b.Code != constants.CodeConflict {
			failRule(t, rule, "期望 409/%d，实际 %d/%d %s", constants.CodeConflict, st, b.Code, b.Msg)
		}
	})

	t.Run("已预约机位登记被409拒绝", func(t *testing.T) {
		const rule = "R5 HTTP 已预约不能登记"
		st, b := e.do("admin", http.MethodPost, "/api/v1/stations/3/repair-report", map[string]string{"reason": "故障"})
		if st != http.StatusConflict || b.Code != constants.CodeConflict {
			failRule(t, rule, "期望 409/%d，实际 %d/%d %s", constants.CodeConflict, st, b.Code, b.Msg)
		}
	})

	t.Run("不存在机位登记返回404", func(t *testing.T) {
		const rule = "R5 HTTP 机位不存在"
		st, b := e.do("admin", http.MethodPost, "/api/v1/stations/999/repair-report", map[string]string{"reason": "故障"})
		if st != http.StatusNotFound || b.Code != constants.CodeNotFound {
			failRule(t, rule, "期望 404/%d，实际 %d/%d", constants.CodeNotFound, st, b.Code)
		}
	})

	t.Run("故障但无待处理报修单不能关闭", func(t *testing.T) {
		const rule = "R4 HTTP 无待处理记录不能关闭"
		// station 4 预置为 fault 但没有任何报修记录
		st, b := e.do("admin", http.MethodPost, "/api/v1/stations/4/repair-close", map[string]string{"handle_result": "修好了"})
		if st != http.StatusConflict || b.Code != constants.CodeRepairNone {
			failRule(t, rule, "期望 409/%d，实际 %d/%d %s", constants.CodeRepairNone, st, b.Code, b.Msg)
		}
	})
}

// TestHTTPRuleInvalidEnumAndBypass R10：非法状态枚举拒绝；旧状态接口不能绕过报修闭环。
func TestHTTPRuleInvalidEnumAndBypass(t *testing.T) {
	e := newHTTPEnv(t)

	t.Run("非法状态枚举被400拒绝", func(t *testing.T) {
		const rule = "R10 HTTP 非法状态枚举"
		st, b := e.do("admin", http.MethodPut, "/api/v1/stations/1/status", map[string]string{"status": "broken"})
		if st != http.StatusBadRequest || b.Code != constants.CodeValidation {
			failRule(t, rule, "期望 400/%d，实际 %d/%d %s", constants.CodeValidation, st, b.Code, b.Msg)
		}
	})

	t.Run("旧接口idle到fault被拒绝", func(t *testing.T) {
		const rule = "R10 HTTP 旧接口不能绕过登记"
		st, b := e.do("staff", http.MethodPut, "/api/v1/stations/1/status", map[string]string{"status": "fault"})
		if st != http.StatusConflict || b.Code != constants.CodeRepairOpen {
			failRule(t, rule, "期望 409/%d，实际 %d/%d %s", constants.CodeRepairOpen, st, b.Code, b.Msg)
		}
	})

	t.Run("旧接口fault到idle被拒绝", func(t *testing.T) {
		const rule = "R10 HTTP 旧接口不能绕过关闭"
		// 先通过闭环把 1 号机位置为故障
		if st, _ := e.do("staff", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{"reason": "鼠标失灵"}); st != http.StatusOK {
			t.Fatalf("[测试基建] 前置登记失败: %d", st)
		}
		st, b := e.do("staff", http.MethodPut, "/api/v1/stations/1/status", map[string]string{"status": "idle"})
		if st != http.StatusConflict || b.Code != constants.CodeRepairNone {
			failRule(t, rule, "期望 409/%d，实际 %d/%d %s", constants.CodeRepairNone, st, b.Code, b.Msg)
		}
	})

	t.Run("原有reserved到idle操作保持可用", func(t *testing.T) {
		const rule = "R10 HTTP 原有状态操作不回归"
		st, b := e.do("admin", http.MethodPut, "/api/v1/stations/3/status", map[string]string{"status": "idle"})
		if st != http.StatusOK || b.Code != constants.CodeOK {
			failRule(t, rule, "reserved->idle 应保持可用，实际 %d/%d %s", st, b.Code, b.Msg)
		}
	})

	t.Run("fault到reserved非法流转被409拒绝", func(t *testing.T) {
		const rule = "R10 HTTP 故障机位不能流转到预约"
		// 1 号机位当前为 fault
		st, b := e.do("admin", http.MethodPut, "/api/v1/stations/1/status", map[string]string{"status": "reserved"})
		if st != http.StatusConflict || b.Code != constants.CodeConflict {
			failRule(t, rule, "期望 409/%d，实际 %d/%d %s", constants.CodeConflict, st, b.Code, b.Msg)
		}
	})
}

// TestHTTPRuleRepairList 报修管理列表的权限与筛选。
func TestHTTPRuleRepairList(t *testing.T) {
	e := newHTTPEnv(t)

	if st, _ := e.do("staff", http.MethodPost, "/api/v1/stations/1/repair-report", map[string]string{"reason": "待处理单"}); st != http.StatusOK {
		t.Fatalf("[测试基建] 登记失败: %d", st)
	}

	t.Run("管理员可按pending筛选", func(t *testing.T) {
		const rule = "R3 HTTP 待处理列表唯一"
		_, b := e.do("admin", http.MethodGet, "/api/v1/repairs?status=pending&page=1&page_size=100", nil)
		if b.Code != constants.CodeOK {
			failRule(t, rule, "列表查询失败: %d %s", b.Code, b.Msg)
		}
		var page struct {
			List  []model.RepairRecord `json:"list"`
			Total int64                `json:"total"`
		}
		_ = json.Unmarshal(b.Data, &page)
		if page.Total != 1 || len(page.List) != 1 {
			failRule(t, rule, "pending 应仅 1 条: total=%d len=%d", page.Total, len(page.List))
		}
	})

	t.Run("非法状态筛选被400拒绝", func(t *testing.T) {
		const rule = "R10 HTTP 非法筛选枚举"
		st, b := e.do("admin", http.MethodGet, "/api/v1/repairs?status=done", nil)
		if st != http.StatusBadRequest || b.Code != constants.CodeValidation {
			failRule(t, rule, "期望 400/%d，实际 %d/%d", constants.CodeValidation, st, b.Code)
		}
	})
}
