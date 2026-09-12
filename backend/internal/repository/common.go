package repository

import (
	"errors"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// clauseLocking 返回 MySQL 行锁子句，并发扣款/状态流转时使用 SELECT ... FOR UPDATE。
func clauseLocking() clause.Locking {
	return clause.Locking{Strength: "UPDATE"}
}

// IsDuplicateKeyErr 判断是否唯一键冲突（MySQL 1062 / GORM 翻译错误）。
func IsDuplicateKeyErr(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}
	return errors.Is(err, gorm.ErrDuplicatedKey)
}
