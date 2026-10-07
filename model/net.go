package model

import (
	"errors"
	"github.com/lanthora/cacao/logger"
	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

func init() {
	db := storage.Get()
	err := db.AutoMigrate(Net{})
	if err != nil {
		logger.Fatal("auto migrate nets failed: %v", err)
	}
}

type Net struct {
	gorm.Model
	UserID    uint   `gorm:"index:idx_net"`
	Name      string `gorm:"index:idx_net"`
	Password  string
	DHCP      string
	Broadcast bool
	Lease     uint
}

func (n *Net) Create() error {
	return storage.Write(func(tx *gorm.DB) error {
		var existing Net
		err := tx.Where("user_id = ? AND name = ?", n.UserID, n.Name).Take(&existing).Error
		if err == nil {
			if existing.Password != n.Password || existing.DHCP != n.DHCP || existing.Broadcast != n.Broadcast || existing.Lease != n.Lease {
				return ErrConflict
			}
			*n = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var user User
		if err := tx.Where("id = ?", n.UserID).Take(&user).Error; err != nil {
			return err
		}
		return tx.Create(n).Error
	})
}

func (n *Net) Update() error {
	return storage.Write(func(tx *gorm.DB) error {
		var current Net
		if err := tx.Where("id = ? AND user_id = ?", n.ID, n.UserID).Take(&current).Error; err != nil {
			return err
		}
		var count int64
		if current.Name != n.Name {
			if err := tx.Model(&Net{}).Where("user_id = ? AND name = ? AND id <> ?", n.UserID, n.Name, n.ID).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return ErrConflict
			}
		}
		if current.Name == n.Name && current.Password == n.Password && current.DHCP == n.DHCP && current.Broadcast == n.Broadcast && current.Lease == n.Lease {
			return nil
		}
		return tx.Model(&Net{}).Where("id = ?", n.ID).Select("name", "password", "dhcp", "broadcast", "lease").Updates(n).Error
	})
}

func (n *Net) Delete() error {
	return DeleteNetworkTree(n.ID, n.UserID)
}

func GetNets() (nets []Net) {
	db := storage.Get()
	db.Model(&Net{}).Joins("join users on users.id = nets.user_id AND users.deleted_at IS NULL").Find(&nets)
	return
}

func GetNetByNetID(netid uint) (net Net) {
	if netid != 0 {
		db := storage.Get()
		db.Where(&Net{Model: gorm.Model{ID: netid}}).Take(&net)
	}
	return
}

func GetNetsByUserID(userid uint) (nets []Net) {
	if userid != 0 {
		db := storage.Get()
		db.Where(&Net{UserID: userid}).Find(&nets)
	}
	return
}

func GetNetIdByUsernameAndNetname(username, netname string) uint {
	netid := uint(0)
	db := storage.Get()
	db.Model(&Net{}).Select("nets.id").Joins("join users on users.id = nets.user_id AND users.deleted_at IS NULL").Where("users.name = ? and nets.name = ?", username, netname).Take(&netid)
	return netid
}

func DeleteNetByNetID(netid uint) error {
	network := GetNetByNetID(netid)
	return DeleteNetworkTree(netid, network.UserID)
}
