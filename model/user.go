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

func (u *User) Save() {
	db := storage.Get()
	db.Save(u)
}

func (u *User) Delete() {
	db := storage.Get()
	db.Delete(u)
}

func GetUsers() (users []User) {
	db := storage.Get()
	db.Find(&users)
	return
}

func DeleteUserByUserID(userid uint) {
	db := storage.Get()
	db.Delete(&User{Model: gorm.Model{ID: userid}})
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

func RefreshUserLastActiveTimeByUserID(userid uint) {
	if userid != 0 {
		db := storage.Get()
		// Updating activity must never restore a concurrently revoked session.
		db.Model(&User{}).Where("id = ?", userid).UpdateColumn("updated_at", time.Now())
	}
}
