package db

import (
	"github.com/0glabs/0g-serving-broker/inference/model"
)

// CreateVideoJobOwner records the (provider job id -> creator address) mapping used to
// authorize GET /videos/{id} and GET /videos/{id}/content — see model.VideoJobOwner and
// issue #591.
func (d *DB) CreateVideoJobOwner(providerJobID, userAddress string) error {
	return d.db.Create(&model.VideoJobOwner{
		ProviderJobID: providerJobID,
		UserAddress:   userAddress,
	}).Error
}

// GetVideoJobOwner retrieves the recorded creator address for a provider job id. Returns
// gorm.ErrRecordNotFound when no owner was ever recorded (an unknown id, or a job created
// before ownership tracking existed) — callers must treat that the same as a mismatched
// owner, not as "allow". See ctrl.AuthorizeVideoJobAccess.
func (d *DB) GetVideoJobOwner(providerJobID string) (model.VideoJobOwner, error) {
	var owner model.VideoJobOwner
	err := d.db.Where("provider_job_id = ?", providerJobID).First(&owner).Error
	return owner, err
}
