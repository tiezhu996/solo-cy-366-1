package handler

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/esportsbar/backend/internal/constants"
	"github.com/esportsbar/backend/internal/dto"
	"github.com/esportsbar/backend/internal/service"
	"github.com/esportsbar/backend/internal/util"
	"github.com/esportsbar/backend/pkg/response"
)

// RepairHandler 机位报修闭环接口处理器。
type RepairHandler struct {
	repairService *service.RepairService
	logger        *slog.Logger
}

// NewRepairHandler 构造报修接口处理器。
func NewRepairHandler(repairService *service.RepairService, logger *slog.Logger) *RepairHandler {
	return &RepairHandler{repairService: repairService, logger: logger}
}

// Report 登记报修（管理员/店员标记故障，填写原因）。
func (h *RepairHandler) Report(c *gin.Context) {
	var idReq dto.IDReq
	if err := c.ShouldBindUri(&idReq); err != nil {
		response.Fail(c, 400, constants.CodeValidation, "机位 ID 无效")
		return
	}
	var req dto.CreateRepairReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, constants.CodeValidation, "登记报修参数校验失败（实体：repair_record，字段：reason）："+err.Error())
		return
	}
	op := operatorFromContext(c)
	rec, station, err := h.repairService.Report(op, idReq.ID, req.Reason)
	if err != nil {
		h.abort(c, fmt.Errorf("repair handler report stationID=%d role=%s: %w", idReq.ID, op.Role, err))
		return
	}
	response.OKMessage(c, constants.MsgRepairCreateOK, gin.H{"repair": rec, "station": station})
}

// Close 关闭报修（机位恢复空闲，填写处理结果）。
func (h *RepairHandler) Close(c *gin.Context) {
	var idReq dto.IDReq
	if err := c.ShouldBindUri(&idReq); err != nil {
		response.Fail(c, 400, constants.CodeValidation, "机位 ID 无效")
		return
	}
	var req dto.CloseRepairReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, 400, constants.CodeValidation, "关闭报修参数校验失败（实体：repair_record，字段：handle_result）："+err.Error())
		return
	}
	op := operatorFromContext(c)
	rec, station, legacy, err := h.repairService.Close(op, idReq.ID, req.HandleResult)
	if err != nil {
		h.abort(c, fmt.Errorf("repair handler close stationID=%d role=%s: %w", idReq.ID, op.Role, err))
		return
	}
	if legacy {
		// 故障机位无待处理报修单（历史遗留），已凭处理结果补录已关闭记录并恢复空闲
		response.OKMessage(c, "机位已恢复空闲（无待处理报修单，已补录已关闭报修记录）", gin.H{"repair": rec, "station": station, "legacy": legacy})
		return
	}
	response.OKMessage(c, constants.MsgRepairCloseOK, gin.H{"repair": rec, "station": station, "legacy": legacy})
}

// ListByStation 查询机位报修记录（机位详情使用）。
func (h *RepairHandler) ListByStation(c *gin.Context) {
	var idReq dto.IDReq
	if err := c.ShouldBindUri(&idReq); err != nil {
		response.Fail(c, 400, constants.CodeValidation, "机位 ID 无效")
		return
	}
	list, err := h.repairService.ListByStation(idReq.ID)
	if err != nil {
		h.abort(c, err)
		return
	}
	response.OK(c, list)
}

// Detail 机位详情：机位信息 + 待处理报修 + 报修历史（原因/结果/角色/时间）。
func (h *RepairHandler) Detail(c *gin.Context) {
	var idReq dto.IDReq
	if err := c.ShouldBindUri(&idReq); err != nil {
		response.Fail(c, 400, constants.CodeValidation, "机位 ID 无效")
		return
	}
	detail, err := h.repairService.Detail(idReq.ID)
	if err != nil {
		h.abort(c, err)
		return
	}
	response.OK(c, detail)
}

// List 报修记录分页列表。
func (h *RepairHandler) List(c *gin.Context) {
	var query dto.RepairQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		response.Fail(c, 400, constants.CodeValidation, "报修查询参数校验失败："+err.Error())
		return
	}
	list, total, err := h.repairService.List(&query)
	if err != nil {
		h.abort(c, err)
		return
	}
	response.OK(c, dto.PageResult{List: list, Total: total, Page: query.Page, PageSize: query.PageSize})
}

// Get 报修记录详情。
func (h *RepairHandler) Get(c *gin.Context) {
	var idReq dto.IDReq
	if err := c.ShouldBindUri(&idReq); err != nil {
		response.Fail(c, 400, constants.CodeValidation, "报修记录 ID 无效")
		return
	}
	rec, err := h.repairService.GetByID(idReq.ID)
	if err != nil {
		h.abort(c, err)
		return
	}
	response.OK(c, rec)
}

// operatorFromContext 从 JWT 上下文提取操作人。
func operatorFromContext(c *gin.Context) service.Operator {
	uid, _ := c.Get("user_id")
	uname, _ := c.Get("username")
	role, _ := c.Get("role")
	userID, _ := uid.(uint)
	username, _ := uname.(string)
	roleStr, _ := role.(string)
	return service.Operator{UserID: userID, Username: username, Role: roleStr}
}

// abort 统一错误处理（handler 再次包装 service 错误）。
func (h *RepairHandler) abort(c *gin.Context, err error) {
	var appErr *util.AppError
	if errors.As(err, &appErr) {
		response.Fail(c, httpStatusFor(appErr.Code), appErr.Code, appErr.Message)
		return
	}
	h.logger.Error(fmt.Sprintf("repair handler error: %v", err))
	response.Fail(c, 500, constants.CodeInternal, "服务器内部错误，请稍后重试")
}
