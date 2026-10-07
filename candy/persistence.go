package candy

import (
	"errors"

	"github.com/lanthora/cacao/model"
	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

// SyncNet reads committed state under the cache lock. Concurrent API responses
// cannot publish an older network configuration after a newer one.
func SyncNet(netID uint) error { return syncNet(netID, false) }

func syncNet(netID uint, force bool) error {
	idNetMapMutex.Lock()
	defer idNetMapMutex.Unlock()
	var current model.Net
	err := storage.Get().Model(&model.Net{}).Joins("JOIN users ON users.id = nets.user_id AND users.deleted_at IS NULL").Where("nets.id = ?", netID).Take(&current).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	existing := idNetMap[netID]
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if existing != nil {
			existing.close()
			delete(idNetMap, netID)
		}
		return nil
	}
	if existing != nil && !force {
		old := existing.model
		if old.Name == current.Name && old.Password == current.Password && old.DHCP == current.DHCP && old.Broadcast == current.Broadcast && old.Lease == current.Lease {
			return nil
		}
	}
	if existing != nil {
		existing.close()
	}
	insertNetLocked(&current)
	return nil
}
