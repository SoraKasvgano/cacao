package storage

import (
	"os"
	"path"

	"github.com/glebarez/sqlite"
	"github.com/lanthora/cacao/argp"
	"github.com/lanthora/cacao/logger"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func init() {
	storageDir := argp.Get("storage", ".")
	err := os.MkdirAll(storageDir, 0700)
	if err != nil {
		logger.Fatal("make storage dir failed: %v", err)
	}
	databasePath := path.Join(storageDir, "sqlite.db")
	// SQLite contains password hashes and session credentials. Restrict new
	// files while preserving the permissions of existing deployment volumes.
	file, err := os.OpenFile(databasePath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		logger.Fatal("create storage database failed: %v", err)
	}
	if err := file.Close(); err != nil {
		logger.Fatal("close storage database failed: %v", err)
	}
	db, err = gorm.Open(sqlite.Open(databasePath), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		logger.Fatal("open storage database failed: %v", err)
	}

	logger.Info("storage=[%v]", storageDir)
}

var db *gorm.DB

func Get() *gorm.DB {
	return db
}
