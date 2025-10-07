package db

import (
	"strings"
	"time"

	"github.com/pkg/errors"

	constant "github.com/0glabs/0g-serving-broker/inference/const"
	"github.com/0glabs/0g-serving-broker/inference/model"
)

func (d *DB) GetUserAccount(userAddress string) (model.User, error) {
	account := model.User{}
	ret := d.db.Where(&model.User{User: userAddress}).First(&account)
	return account, ret.Error
}

func (d *DB) CreateUserAccounts(accounts []model.User) error {
	if len(accounts) == 0 {
		return nil
	}
	ret := d.db.Create(&accounts)
	return ret.Error
}

func (d *DB) ListUserAccount(opt *model.UserListOptions) ([]model.User, error) {
	tx := d.db.Model(model.User{})

	if opt != nil {
		if opt.LowBalanceRisk != nil && opt.SettleTriggerThreshold != nil {
			tx = tx.Where("((lock_balance - unsettled_fee) < ? OR last_balance_check_time < ?)", constant.SettleTriggerThreshold, *opt.LowBalanceRisk)
		}
		if opt.MinUnsettledFee != nil {
			tx = tx.Where("unsettled_fee > ?", *opt.MinUnsettledFee)
		}
	}
	list := []model.User{}
	ret := tx.Find(&list)
	return list, ret.Error
}

func (d *DB) DeleteUserAccounts(userAddresses []string) error {
	if len(userAddresses) == 0 {
		return nil
	}
	return d.db.Where("user IN (?)", userAddresses).Delete(&model.User{}).Error
}

func (d *DB) UpdateUserAccount(userAddress string, new model.User) error {
	old := model.User{}
	ret := d.db.Where(&model.User{User: userAddress}).First(&old)
	if ret.Error != nil {
		return errors.Wrap(ret.Error, "get account from db")
	}
	if new.LastBalanceCheckTime != nil {
		old.LastBalanceCheckTime = new.LastBalanceCheckTime
	}
	if new.LockBalance != nil {
		old.LockBalance = new.LockBalance
	}

	ret = d.db.Where(&model.User{User: old.User}).Updates(old)
	return ret.Error
}

func (d *DB) BatchUpdateUserAccount(news []model.User) error {
	olds, err := d.ListUserAccount(nil)
	if err != nil {
		return err
	}
	oldAccountMap := make(map[string]bool, len(olds))
	for _, old := range olds {
		oldAccountMap[strings.ToLower(old.User)] = true
	}

	var toAdd, toUpdate []model.User
	var toRemove []string
	for i, new := range news {
		key := strings.ToLower(new.User)
		if oldAccountMap[key] {
			delete(oldAccountMap, key)
			// BatchUpdateUserAccount is currently used to synchronize accounts from the contract to the database.
			// All new data should be updated in the database since each record has a new LastBalanceCheckTime.
			toUpdate = append(toUpdate, news[i])
			continue
		}
		toAdd = append(toAdd, news[i])
	}
	for k := range oldAccountMap {
		toRemove = append(toRemove, k)
	}

	// TODO: add Redis RW lock
	if err := d.CreateUserAccounts(toAdd); err != nil {
		return err
	}
	for i := range toUpdate {
		if ret := d.db.Where(&model.User{User: toUpdate[i].User}).Updates(toUpdate[i]); ret.Error != nil {
			return ret.Error
		}
	}
	return d.DeleteUserAccounts(toRemove)
}

func (d *DB) ListUsersWithUnsettledFees(opt *model.UserListOptions, inputPrice, outputPrice int64) ([]model.User, error) {
	if opt == nil {
		opt = &model.UserListOptions{}
	}

	// Build the optimized query with JOIN and aggregation
	query := `
		SELECT 
			u.user,
			u.lock_balance,
			u.last_balance_check_time,
			COALESCE(SUM(r.input_count * ? + r.output_count * ?), 0) as calculated_unsettled_fee
		FROM user u
		LEFT JOIN request r ON u.user = r.user_address AND r.processed = false
		WHERE (u.skip_until IS NULL OR u.skip_until <= ?)
	`
	args := []interface{}{inputPrice, outputPrice, time.Now()}

	// Group by user fields
	query += " GROUP BY u.user, u.lock_balance, u.last_balance_check_time"

	// Add HAVING clause for filtering - maintain original OR logic
	havingClauses := []string{}

	if opt.MinUnsettledFee != nil {
		havingClauses = append(havingClauses, "calculated_unsettled_fee > ?")
		args = append(args, *opt.MinUnsettledFee)
	}

	// Preserve original OR logic: (balance_condition OR time_condition)
	if opt.LowBalanceRisk != nil && opt.SettleTriggerThreshold != nil {
		havingClauses = append(havingClauses,
			"((CAST(u.lock_balance AS SIGNED) - calculated_unsettled_fee) < ? OR u.last_balance_check_time < ?)")
		args = append(args, *opt.SettleTriggerThreshold, *opt.LowBalanceRisk)
	} else if opt.SettleTriggerThreshold != nil {
		// Only check balance condition if no time filter - match original logic
		havingClauses = append(havingClauses,
			"(CAST(u.lock_balance AS SIGNED) - calculated_unsettled_fee) < ?")
		args = append(args, *opt.SettleTriggerThreshold)
	} else if opt.LowBalanceRisk != nil {
		// Only check time condition if no threshold
		havingClauses = append(havingClauses, "u.last_balance_check_time < ?")
		args = append(args, *opt.LowBalanceRisk)
	}

	if len(havingClauses) > 0 {
		query += " HAVING " + havingClauses[0]
		for i := 1; i < len(havingClauses); i++ {
			query += " AND " + havingClauses[i]
		}
	}

	// Execute the query
	type QueryResult struct {
		User                   string
		LockBalance            *string
		LastBalanceCheckTime   *time.Time
		CalculatedUnsettledFee int64
	}

	var results []QueryResult
	if err := d.db.Raw(query, args...).Scan(&results).Error; err != nil {
		return nil, errors.Wrap(err, "query users with unsettled fees")
	}

	// Convert results to User models
	users := make([]model.User, 0, len(results))
	for _, r := range results {
		users = append(users, model.User{
			User:                 r.User,
			LockBalance:          r.LockBalance,
			LastBalanceCheckTime: r.LastBalanceCheckTime,
		})
	}

	return users, nil
}

// UpdateUserSkipUntil updates the skip_until field for a specific user
func (d *DB) UpdateUserSkipUntil(userAddress string, skipUntil *time.Time) error {
	return d.db.Model(&model.User{}).
		Where("user = ?", userAddress).
		Update("skip_until", skipUntil).Error
}

// ClearExpiredUserSkipUntil clears the skip_until field for users whose skip period has expired
func (d *DB) ClearExpiredUserSkipUntil() error {
	now := time.Now()
	return d.db.Model(&model.User{}).
		Where("skip_until IS NOT NULL AND skip_until <= ?", now).
		Update("skip_until", nil).Error
}

