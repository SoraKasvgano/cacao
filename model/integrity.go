package model

import (
	"errors"
	"time"

	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

var ErrConflict = errors.New("active record already exists")

// EnsureIntegrity installs safeguards without deleting or merging ambiguous
// legacy identities. Maintenance reports existing duplicates for operator review.

// DeleteUserTree is atomic and safe to repeat. Network connections are revoked
// by the caller only after this transaction has committed.
func DeleteUserTree(userID uint) ([]uint, error) {
	if userID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var netIDs []uint
	err := storage.Write(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Model(&Net{}).Where("user_id = ?", userID).Pluck("id", &netIDs).Error; err != nil {
			return err
		}
		if len(netIDs) > 0 {
			// Subqueries avoid SQLite's parameter limit for accounts with many nets.
			nets := tx.Unscoped().Model(&Net{}).Select("id").Where("user_id = ?", userID)
			if err := tx.Where("net_id IN (?)", nets).Delete(&Device{}).Error; err != nil {
				return err
			}
			if err := tx.Where("net_id IN (?)", nets).Delete(&Route{}).Error; err != nil {
				return err
			}
			if err := tx.Where("user_id = ?", userID).Delete(&Net{}).Error; err != nil {
				return err
			}
		}
		return tx.Where("id = ?", userID).Delete(&User{}).Error
	})
	return netIDs, err
}

func DeleteNetworkTree(netID, ownerID uint) error {
	if netID == 0 || ownerID == 0 {
		return gorm.ErrRecordNotFound
	}
	return storage.Write(func(tx *gorm.DB) error {
		var network Net
		if err := tx.Unscoped().Where("id = ? AND user_id = ?", netID, ownerID).Take(&network).Error; err != nil {
			return err
		}
		if err := tx.Where("net_id = ?", netID).Delete(&Device{}).Error; err != nil {
			return err
		}
		if err := tx.Where("net_id = ?", netID).Delete(&Route{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", netID).Delete(&Net{}).Error
	})
}

// SetConfigTx is usable inside a larger business transaction. Unchanged
// settings perform no UPDATE, so repeated requests do not generate WAL traffic.
func SetConfigTx(tx *gorm.DB, key, value string) error {
	var current Config
	err := tx.Where("key = ?", key).Order("id").Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&Config{Key: key, Value: value}).Error
	}
	if err != nil {
		return err
	}
	if current.Value == value {
		return nil
	}
	return tx.Model(&Config{}).Where("id = ?", current.ID).Updates(map[string]interface{}{"value": value, "updated_at": time.Now()}).Error
}
