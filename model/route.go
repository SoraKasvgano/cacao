package model

import (
	"errors"
	"github.com/lanthora/cacao/logger"
	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

func init() {
	db := storage.Get()
	err := db.AutoMigrate(Route{})
	if err != nil {
		logger.Fatal("auto migrate routes failed: %v", err)
	}
}

type Route struct {
	gorm.Model
	NetID    uint `gorm:"index"`
	DevAddr  string
	DevMask  string
	DstAddr  string
	DstMask  string
	NextHop  string
	Priority int
}

func (r *Route) Create() error {
	_, err := r.CreateOnce()
	return err
}

func (r *Route) CreateOnce() (bool, error) {
	created := false
	err := storage.Write(func(tx *gorm.DB) error {
		var network Net
		if err := tx.Model(&Net{}).Joins("JOIN users ON users.id = nets.user_id AND users.deleted_at IS NULL").Where("nets.id = ?", r.NetID).Take(&network).Error; err != nil {
			return err
		}
		var existing Route
		err := tx.Where("net_id = ? AND dev_addr = ? AND dev_mask = ? AND dst_addr = ? AND dst_mask = ? AND next_hop = ? AND priority = ?", r.NetID, r.DevAddr, r.DevMask, r.DstAddr, r.DstMask, r.NextHop, r.Priority).Take(&existing).Error
		if err == nil {
			*r = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Create(r).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	return created && err == nil, err
}

func (r *Route) Delete() error {
	if r.ID == 0 {
		return gorm.ErrRecordNotFound
	}
	return storage.Write(func(tx *gorm.DB) error {
		return tx.Where("id = ? AND net_id = ?", r.ID, r.NetID).Delete(&Route{}).Error
	})
}

func GetRouteByRouteID(routeid uint) (route Route) {
	if routeid != 0 {
		db := storage.Get()
		db.Where(&Route{Model: gorm.Model{ID: routeid}}).Take(&route)
	}
	return
}

func GetRoutesByUserID(userid uint) (routes []Route) {
	if userid != 0 {
		db := storage.Get()
		db.Model(&Route{}).Joins("join nets on routes.net_id = nets.id AND nets.deleted_at IS NULL").Where("nets.user_id = ?", userid).Order("routes.net_id,routes.priority").Find(&routes)
	}
	return
}
