package model

import (
	"github.com/lanthora/cacao/logger"
	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

func init() {
	db := storage.Get()

	if err := db.AutoMigrate(Config{}); err != nil {
		logger.Fatal("auto migrate configs failed: %v", err)
	}
}

type Config struct {
	gorm.Model
	Key   string `gorm:"index"`
	Value string
}

func (c *Config) Save() error {
	return SetConfig(c.Key, c.Value)
}

func SetConfig(key string, value string) error {
	return storage.Write(func(tx *gorm.DB) error { return SetConfigTx(tx, key, value) })
}

func GetConfig(key string, defaultValue string) string {
	db := storage.Get()
	config := &Config{Key: key}
	if result := db.Where(config).Take(config); result.Error == nil {
		return config.Value
	}
	return defaultValue
}

func DelConfig(key string) error {
	return storage.Write(func(tx *gorm.DB) error { return tx.Where("key = ?", key).Delete(&Config{}).Error })
}
