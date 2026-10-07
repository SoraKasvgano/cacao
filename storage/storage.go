package storage

import (
	"context"
	"os"
	"path/filepath"

	"github.com/lanthora/cacao/argp"
	"github.com/lanthora/cacao/logger"
	"gorm.io/gorm"
)

func init() {
	storageDir := argp.Get("storage", ".")
	err := os.MkdirAll(storageDir, 0700)
	if err != nil {
		logger.Fatal("make storage dir failed: %v", err)
	}
	databasePath := filepath.Join(storageDir, "sqlite.db")
	// SQLite contains password hashes and session credentials. Restrict new
	// files while preserving the permissions of existing deployment volumes.
	file, err := os.OpenFile(databasePath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		logger.Fatal("create storage database failed: %v", err)
	}
	if err := file.Close(); err != nil {
		logger.Fatal("close storage database failed: %v", err)
	}
	database, err = openDatabase(databasePath)
	if err != nil {
		logger.Fatal("open storage database failed: %v", err)
	}

	logger.Info("storage=[%v]", storageDir)
}

var database *store

// Get returns the bounded read pool. Runtime mutations must use Write; direct
// writes are reserved for startup migrations and test fixture setup.
func Get() *gorm.DB {
	return database.reader
}

// Write serializes a logical mutation and acknowledges it only after durable
// commit. Concurrent mutations share a short group-commit window. The callback
// must use its tx for ALL reads and writes, return every database error, and must
// not call Write/Flush recursively or perform external side effects. A callback
// may be rolled back even after it returned nil if the outer commit fails.
func Write(fn func(*gorm.DB) error) error {
	return WriteContext(context.Background(), fn)
}

// WriteContext allows cancellation while waiting for queue space or execution.
// Once execution starts, it waits for the definitive commit/rollback result;
// cancellation cannot turn a committed write into an ambiguous timeout response.
func WriteContext(ctx context.Context, fn func(*gorm.DB) error) error {
	return database.write(ctx, fn)
}

// Flush waits until all mutations accepted before its barrier have completed.
func Flush(ctx context.Context) error {
	return database.flush(ctx)
}

// Shutdown stops accepting mutations, drains accepted work, and closes both
// pools. A context timeout stops waiting; draining continues in the background.
// Call after HTTP/WebSocket handlers and background producers have stopped.
func Shutdown(ctx context.Context) error {
	return database.shutdown(ctx)
}

// Statistics describes this process's accepted write jobs. Flush barriers are
// excluded; jobs canceled after admission count as failed. QueueDepth excludes
// the batch currently being collected or executed. Counters reset on restart.
type Statistics struct {
	QueueDepth       int    `json:"queueDepth"`
	BatchesCommitted uint64 `json:"batchesCommitted"`
	JobsCommitted    uint64 `json:"jobsCommitted"`
	JobsFailed       uint64 `json:"jobsFailed"`
	LastBatchSize    uint64 `json:"lastBatchSize"`
}

// Stats returns a nonblocking snapshot without issuing database queries.
func Stats() Statistics {
	return database.stats()
}
