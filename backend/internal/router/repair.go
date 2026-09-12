package router

import (
	"github.com/gin-gonic/gin"

	"github.com/esportsbar/backend/internal/constants"
	"github.com/esportsbar/backend/internal/handler"
	"github.com/esportsbar/backend/internal/middleware"
)

// RegisterRepair 注册机位报修闭环路由。
func RegisterRepair(rg *gin.RouterGroup, h *handler.RepairHandler, jwtSecret string) {
	// 报修记录查询：登录用户可查看（机位详情页展示原因/结果/角色/时间）。
	repairs := rg.Group("/repairs", middleware.Auth(jwtSecret))
	{
		repairs.GET("", middleware.RBAC(constants.RoleAdmin, constants.RoleStaff), h.List)
		repairs.GET("/:id", h.Get)
	}
	// 机位维度的报修动作：仅管理员/店员可登记与关闭，普通会员禁止。
	stations := rg.Group("/stations", middleware.Auth(jwtSecret))
	{
		stations.GET("/:id/repairs", h.ListByStation)
		stations.GET("/:id/detail", h.Detail)
		stations.POST("/:id/repair-report", middleware.RBAC(constants.RoleAdmin, constants.RoleStaff), h.Report)
		stations.POST("/:id/repair-close", middleware.RBAC(constants.RoleAdmin, constants.RoleStaff), h.Close)
	}
}
