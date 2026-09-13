package repository

import (
	"errors"

	"gorm.io/gorm"

	"github.com/esportsbar/backend/internal/model"
)

// RepairRepository 机位报修记录仓储。
type RepairRepository struct {
	db *gorm.DB
}

// NewRepairRepository 构造报修记录仓储。
func NewRepairRepository(db *gorm.DB) *RepairRepository {
	return &RepairRepository{db: db}
}

// Create 写入报修记录。
func (r *RepairRepository) Create(rec *model.RepairRecord) error {
	return r.db.Create(rec).Error
}

// Update 更新报修记录。
func (r *RepairRepository) Update(rec *model.RepairRecord) error {
	return r.db.Save(rec).Error
}

// FindByID 查询报修记录。
func (r *RepairRepository) FindByID(id uint) (*model.RepairRecord, error) {
	var rec model.RepairRecord
	err := r.db.First(&rec, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &rec, err
}

// FindOpenByStation 查询机位当前待处理报修记录；不存在返回 (nil, nil)。
func (r *RepairRepository) FindOpenByStation(stationID uint) (*model.RepairRecord, error) {
	return r.findOpenByStation(r.db, stationID)
}

// LockOpenByStation 事务内行锁查询机位待处理报修记录；不存在返回 (nil, nil)。
func (r *RepairRepository) LockOpenByStation(tx *gorm.DB, stationID uint) (*model.RepairRecord, error) {
	return r.findOpenByStation(tx.Clauses(clauseLocking()), stationID)
}

// LockByID 行锁查询报修记录（关闭报修使用）。
func (r *RepairRepository) LockByID(tx *gorm.DB, id uint) (*model.RepairRecord, error) {
	var rec model.RepairRecord
	err := tx.Clauses(clauseLocking()).First(&rec, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	return &rec, err
}

// ListByStation 查询机位报修记录（详情页最新在前）。
func (r *RepairRepository) ListByStation(stationID uint, limit int) ([]model.RepairRecord, error) {
	var list []model.RepairRecord
	q := r.db.Model(&model.RepairRecord{}).Order("id DESC")
	if stationID > 0 {
		q = q.Where("station_id = ?", stationID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&list).Error
	return list, err
}

// List 分页查询报修记录，支持机位与状态筛选。
func (r *RepairRepository) List(page, pageSize int, stationID uint, status string) ([]model.RepairRecord, int64, error) {
	var list []model.RepairRecord
	var total int64
	query := r.db.Model(&model.RepairRecord{})
	if stationID > 0 {
		query = query.Where("station_id = ?", stationID)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// CountByStation 统计机位的全部报修记录（含待处理与已关闭），删除机位前校验使用。
func (r *RepairRepository) CountByStation(stationID uint) (int64, error) {
	var n int64
	err := r.db.Model(&model.RepairRecord{}).Where("station_id = ?", stationID).Count(&n).Error
	return n, err
}

// findOpenByStation 查询待处理报修记录，内部复用（普通查询/行锁查询复用同一条件）。
func (r *RepairRepository) findOpenByStation(q *gorm.DB, stationID uint) (*model.RepairRecord, error) {
	var rec model.RepairRecord
	err := q.Where("station_id = ? AND status = ?", stationID, "pending").First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}
