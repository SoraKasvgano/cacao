package model

import (
	"time"

	"github.com/lanthora/cacao/logger"
	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

func init() {
	db := storage.Get()
	err := db.AutoMigrate(User{})
	if err != nil {
		logger.Fatal("auto migrate users failed: %v", err)
	}
	// Give existing sessions a one-time grace period when upgrading.
	if err := db.Model(&User{}).Where("token <> ? AND token_expires_at IS NULL", "").UpdateColumn("token_expires_at", time.Now().Add(24*time.Hour)).Error; err != nil {
		logger.Fatal("migrate session expiry failed: %v", err)
	}
}

type User struct {
	gorm.Model
	Name           string `gorm:"index"`
	Password       string
	Token          string
	TokenExpiresAt *time.Time
	Role           string
	IP             string
}

func (u *User) Save() error {
	return storage.Write(func(tx *gorm.DB) error {
		if u.ID == 0 {
			return tx.Create(u).Error
		}
		return tx.Model(&User{}).Where("id = ?", u.ID).Select("name", "password", "token", "token_expires_at", "role", "ip").Updates(u).Error
	})
}

func (u *User) Delete() error {
	_, err := DeleteUserTree(u.ID)
	return err
}

func GetUsers() (users []User) {
	db := storage.Get()
	db.Find(&users)
	return
}

func DeleteUserByUserID(userid uint) error {
	_, err := DeleteUserTree(userid)
	return err
}

func GetLastActiveTimeByUserID(userid uint) time.Time {
	if userid != 0 {
		db := storage.Get()
		u := &User{Model: gorm.Model{ID: userid}}
		if result := db.Model(u).Take(&u); result.Error == nil {
			return u.UpdatedAt
		}
	}
	return time.Now()
}

func RefreshUserLastActiveTimeByUserID(userid uint) error {
	if userid != 0 {
		// Updating activity must never restore a concurrently revoked session.
		return storage.Write(func(tx *gorm.DB) error {
			return tx.Model(&User{}).Where("id = ?", userid).UpdateColumn("updated_at", time.Now()).Error
		})
	}
	return nil
}
