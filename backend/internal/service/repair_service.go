package service

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/esportsbar/backend/internal/constants"
	"github.com/esportsbar/backend/internal/dto"
	"github.com/esportsbar/backend/internal/model"
	"github.com/esportsbar/backend/internal/repository"
	"github.com/esportsbar/backend/internal/util"
)

// Operator 操作人上下文（由 handler 从 JWT 注入，报修记录需留存角色与用户名快照）。
type Operator struct {
	UserID   uint
	Username string
	Role     string
}

// RepairService 机位报修闭环服务：登记报修（机位故障）与关闭报修（恢复空闲）。
type RepairService struct {
	repairRepo  *repository.RepairRepository
	stationRepo *repository.StationRepository
	db          *gorm.DB
	logger      *slog.Logger
}

// NewRepairService 构造报修服务。
func NewRepairService(
	repairRepo *repository.RepairRepository,
	stationRepo *repository.StationRepository,
	db *gorm.DB,
	logger *slog.Logger,
) *RepairService {
	return &RepairService{repairRepo: repairRepo, stationRepo: stationRepo, db: db, logger: logger}
}

// Report 登记报修：机位空闲则置为故障并生成待处理报修记录；机位已故障但无待处理记录时补登记。
// 事务内对机位行锁，同一机位只允许一条待处理报修。
func (s *RepairService) Report(op Operator, stationID uint, reason string) (*model.RepairRecord, *model.Station, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, nil, util.NewAppError(constants.CodeValidation, constants.MsgRepairReasonRequired)
	}
	if len(reason) > 500 {
		return nil, nil, util.NewAppError(constants.CodeValidation, "报修原因最长 500 字（实体：repair_record，字段：reason）")
	}

	var rec *model.RepairRecord
	var station *model.Station
	err := s.db.Transaction(func(tx *gorm.DB) error {
		locked, err := s.stationRepo.LockByID(tx, stationID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return util.NewAppError(constants.CodeNotFound, "机位不存在，无法登记报修")
			}
			return fmt.Errorf("repair report lock station: %w", err)
		}
		from := locked.Status
		switch from {
		case constants.StationIdle:
			locked.Status = constants.StationFault
			if err := tx.Save(locked).Error; err != nil {
				return fmt.Errorf("repair report station fault: %w", err)
			}
		case constants.StationFault:
			// 已是故障：仅允许在没有待处理报修时补登记。
		default:
			return util.NewAppError(constants.CodeConflict,
				fmt.Sprintf("机位当前为「%s」，仅空闲机位可以标记故障报修（实体：station，字段：status）", util.StatusText(from)))
		}

		open, err := s.repairRepo.LockOpenByStation(tx, stationID)
		if err != nil {
			return fmt.Errorf("repair report find open: %w", err)
		}
		if open != nil {
			s.logger.Warn(fmt.Sprintf(constants.LogTemplates["repair_repeat_reject"], stationID, op.Username, op.Role))
			return util.NewAppError(constants.CodeRepairOpen,
				fmt.Sprintf("机位「%s」已有待处理报修（单号 #%d），请先处理并恢复空闲，角色 %s 不能重复登记",
					locked.Name, open.ID, util.RoleText(op.Role)))
		}

		rec = &model.RepairRecord{
			StationID:    stationID,
			Reason:       reason,
			Status:       constants.RepairPending,
			ReportUserID: op.UserID,
			ReportByName: op.Username,
			ReportRole:   op.Role,
			ReportedAt:   time.Now(),
		}
		if err := tx.Create(rec).Error; err != nil {
			if repository.IsDuplicateKeyErr(err) {
				// 并发下唯一索引 uk_repair_open_station 兜底：同机位已有待处理报修。
				return util.NewAppError(constants.CodeRepairOpen,
					fmt.Sprintf("机位「%s」已有待处理报修，请勿重复登记（角色：%s）", locked.Name, util.RoleText(op.Role)))
			}
			return fmt.Errorf("repair report create: %w", err)
		}
		station = locked
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("repair report stationID=%d operator=%s role=%s: %w", stationID, op.Username, op.Role, err)
	}
	s.logger.Info(fmt.Sprintf(constants.LogTemplates["repair_create_ok"], rec.ID, stationID, rec.Reason, op.Username, op.Role))
	return rec, station, nil
}

// Close 关闭报修：机位必须处于故障且存在待处理报修，填写处理结果后置机位为空闲并关闭记录。
func (s *RepairService) Close(op Operator, stationID uint, handleResult string) (*model.RepairRecord, *model.Station, error) {
	handleResult = strings.TrimSpace(handleResult)
	if handleResult == "" {
		return nil, nil, util.NewAppError(constants.CodeValidation, constants.MsgRepairResultRequired)
	}
	if len(handleResult) > 500 {
		return nil, nil, util.NewAppError(constants.CodeValidation, "处理结果最长 500 字（实体：repair_record，字段：handle_result）")
	}

	var rec *model.RepairRecord
	var station *model.Station
	err := s.db.Transaction(func(tx *gorm.DB) error {
		locked, err := s.stationRepo.LockByID(tx, stationID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return util.NewAppError(constants.CodeNotFound, "机位不存在，无法关闭报修")
			}
			return fmt.Errorf("repair close lock station: %w", err)
		}
		if locked.Status != constants.StationFault {
			return util.NewAppError(constants.CodeConflict,
				fmt.Sprintf("机位当前为「%s」不是故障状态，不能关闭报修（实体：station，字段：status）", util.StatusText(locked.Status)))
		}

		open, err := s.repairRepo.LockOpenByStation(tx, stationID)
		if err != nil {
			return fmt.Errorf("repair close find open: %w", err)
		}
		if open == nil {
			return util.NewAppError(constants.CodeRepairNone,
				fmt.Sprintf("机位「%s」没有待处理报修记录，无法填写处理结果并关闭（实体：repair_record，角色：%s）",
					locked.Name, util.RoleText(op.Role)))
		}

		now := time.Now()
		open.Status = constants.RepairClosed
		open.HandleResult = handleResult
		open.HandleUserID = op.UserID
		open.HandleByName = op.Username
		open.HandleRole = op.Role
		open.HandledAt = &now
		if err := tx.Save(open).Error; err != nil {
			return fmt.Errorf("repair close save: %w", err)
		}

		locked.Status = constants.StationIdle
		if err := tx.Save(locked).Error; err != nil {
			return fmt.Errorf("repair close station idle: %w", err)
		}
		rec = open
		station = locked
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("repair close stationID=%d operator=%s role=%s: %w", stationID, op.Username, op.Role, err)
	}
	s.logger.Info(fmt.Sprintf(constants.LogTemplates["repair_close_ok"], rec.ID, stationID, rec.HandleResult, op.Username, op.Role))
	return rec, station, nil
}

// Detail 机位详情装配：机位信息 + 当前待处理报修 + 历史报修记录（机位详情页复用）。
func (s *RepairService) Detail(stationID uint) (*dto.StationDetailResp, error) {
	station, err := s.stationRepo.FindByID(stationID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, util.NewAppError(constants.CodeNotFound, "机位不存在")
		}
		return nil, fmt.Errorf("repair detail station: %w", err)
	}
	open, err := s.repairRepo.FindOpenByStation(stationID)
	if err != nil {
		return nil, fmt.Errorf("repair detail open: %w", err)
	}
	records, err := s.repairRepo.ListByStation(stationID, 20)
	if err != nil {
		return nil, fmt.Errorf("repair detail list: %w", err)
	}
	history := make([]any, len(records))
	for i := range records {
		history[i] = records[i]
	}
	resp := &dto.StationDetailResp{Station: station, OpenRepair: open, RepairRecords: history}
	return resp, nil
}

// HasOpenRepair 查询机位是否存在待处理报修（机位状态流转接口复用判断）。
func (s *RepairService) HasOpenRepair(stationID uint) (bool, error) {
	open, err := s.repairRepo.FindOpenByStation(stationID)
	if err != nil {
		return false, fmt.Errorf("repair has open stationID=%d: %w", stationID, err)
	}
	return open != nil, nil
}

// ListByStation 查询机位报修记录（机位详情复用）。
func (s *RepairService) ListByStation(stationID uint) ([]model.RepairRecord, error) {
	list, err := s.repairRepo.ListByStation(stationID, 20)
	if err != nil {
		return nil, fmt.Errorf("repair list by station: %w", err)
	}
	return list, nil
}

// GetByID 查询报修详情。
func (s *RepairService) GetByID(id uint) (*model.RepairRecord, error) {
	rec, err := s.repairRepo.FindByID(id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, util.NewAppError(constants.CodeNotFound, "报修记录不存在")
		}
		return nil, fmt.Errorf("repair get: %w", err)
	}
	return rec, nil
}

// List 分页查询报修记录。
func (s *RepairService) List(query *dto.RepairQuery) ([]model.RepairRecord, int64, error) {
	page := query.Page
	pageSize := query.PageSize
	if page <= 0 {
		page = constants.DefaultPage
	}
	if pageSize <= 0 {
		pageSize = constants.DefaultPageSize
	}
	list, total, err := s.repairRepo.List(page, pageSize, query.StationID, query.Status)
	if err != nil {
		return nil, 0, fmt.Errorf("repair list: %w", err)
	}
	s.logger.Info(fmt.Sprintf(constants.LogTemplates["repair_list_query"], query.StationID, query.Status))
	return list, total, nil
}
