package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/lanthora/cacao/logger"
	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

var ErrDeviceOnline = errors.New("cannot delete an online device")

func init() {
	db := storage.Get()
	err := db.AutoMigrate(Device{})
	if err != nil {
		logger.Fatal("auto migrate devices failed: %v", err)
	}
	if err := storage.Write(func(tx *gorm.DB) error {
		return tx.Model(&Device{}).Where("online = true").Update("online", false).Error
	}); err != nil {
		logger.Fatal("reset device online state failed: %v", err)
	}
}

type Device struct {
	gorm.Model
	NetID    uint `gorm:"index"`
	VMac     string
	IP       string
	Online   bool
	RX       uint64
	TX       uint64
	OS       string
	Version  string
	Hostname string
	Country  string
	Region   string
}

// Save is reserved for durable business changes. Connection telemetry uses
// QueueDeviceState and the shared background flush instead.
func (d *Device) Save() error {
	return storage.Write(func(tx *gorm.DB) error {
		var parentCount int64
		if err := tx.Model(&Net{}).Joins("JOIN users ON users.id = nets.user_id AND users.deleted_at IS NULL").Where("nets.id = ?", d.NetID).Count(&parentCount).Error; err != nil {
			return err
		}
		if parentCount == 0 {
			return fmt.Errorf("device network is unavailable")
		}
		if d.ID == 0 {
			return tx.Create(d).Error
		}
		return tx.Model(d).Select("ip", "online", "rx", "tx", "os", "version", "hostname", "country", "region").Updates(d).Error
	})
}

func (d *Device) Delete() error {
	if d == nil || d.ID == 0 {
		return gorm.ErrRecordNotFound
	}
	devicePersistenceMutex.Lock()
	defer devicePersistenceMutex.Unlock()
	deviceStates.Lock()
	entry, buffered := deviceStates.entries[d.ID]
	deviceStates.Unlock()
	if err := storage.Write(func(tx *gorm.DB) error {
		var current Device
		if err := tx.Unscoped().First(&current, d.ID).Error; err != nil {
			return err
		}
		if current.DeletedAt.Valid {
			return nil
		}
		if buffered {
			// The persistence lock prevents authentication from changing this
			// generation. An unflushed disconnect is valid; telemetry for other
			// devices continues while this transaction waits for SQLite.
			copyDeviceTelemetry(&current, entry.device)
		}
		if current.Online {
			return ErrDeviceOnline
		}
		if buffered && entry.dirty {
			// Preserve the final counters in the same transaction as deletion.
			if err := tx.Model(&current).Select("online", "rx", "tx", "os", "version", "hostname", "country", "region").Updates(&current).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&current).Error
	}); err != nil {
		return err
	}
	deviceStates.Lock()
	if latest, ok := deviceStates.entries[d.ID]; ok && buffered && latest.generation == entry.generation {
		removeDeviceStateLocked(d.ID)
	}
	deviceStates.Unlock()
	return nil
}

func GetDeviceByDevID(devid uint) (device Device) {
	if devid != 0 {
		db := storage.Get()
		db.Where(&Device{Model: gorm.Model{ID: devid}}).Find(&device)
		OverlayDeviceState(&device)
	}
	return
}

func GetDevicesByNetID(netid uint) (devices []Device) {
	if netid != 0 {
		db := storage.Get()
		db.Where(&Device{NetID: netid}).Find(&devices)
		OverlayDeviceStates(devices)
	}
	return
}

func GetDevicesByUserID(userid uint) (devices []Device) {
	if userid != 0 {
		db := storage.Get()
		db.Model(&Device{}).Joins("join nets on devices.net_id = nets.id AND nets.deleted_at IS NULL").Where("nets.user_id = ?", userid).Find(&devices)
		OverlayDeviceStates(devices)
	}
	return
}

func GetRxSumByUserID(userid uint) (rx uint64) {
	if userid != 0 {
		totals, _ := GetUserTrafficTotals([]uint{userid})
		rx = totals[userid].RX
	}
	return
}

func GetTxSumByUserID(userid uint) (tx uint64) {
	if userid != 0 {
		totals, _ := GetUserTrafficTotals([]uint{userid})
		tx = totals[userid].TX
	}
	return
}

func DeleteDevicesByNetID(netid uint) error {
	if netid == 0 {
		return nil
	}
	return WriteDeletingDevices(context.Background(), 0, netid, func(tx *gorm.DB) error {
		return tx.Where("net_id = ?", netid).Delete(&Device{}).Error
	})
}

func (d *Device) SaveRxTxOnline() {
	db := storage.Get()
	if d.ID == 0 {
		db.Create(d)
	} else {
		db.Model(d).Select("rx", "tx", "online").Updates(d)
	}
}

func (d *Device) SaveOsVersionHostname() {
	db := storage.Get()
	if d.ID == 0 {
		db.Create(d)
	} else {
		db.Model(d).Select("os", "version", "hostname").Updates(d)
	}
}
