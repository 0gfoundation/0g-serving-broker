package db

import (
	"time"

	"gorm.io/gorm/clause"

	"github.com/0glabs/0g-serving-broker/inference/model"
)

func (d *DB) ListAssayPayouts() ([]model.AssayPayout, error) {
	var rows []model.AssayPayout
	err := d.db.Model(&model.AssayPayout{}).Find(&rows).Error
	return rows, err
}

// UpsertAssayPayout writes a node's full payout state (cumulative, epoch,
// pending covered hashes, invoiced flag).
func (d *DB) UpsertAssayPayout(row model.AssayPayout) error {
	now := time.Now()
	row.UpdatedAt = &now
	return d.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "node"}},
		UpdateAll: true,
	}).Create(&row).Error
}
