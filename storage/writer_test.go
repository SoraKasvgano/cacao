package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

type testRecord struct {
	ID    int `gorm:"primaryKey"`
	Value int
}

func testStore(t *testing.T) *store {
	t.Helper()
	s, err := openDatabase(filepath.Join(t.TempDir(), "database with spaces.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	if err := s.reader.AutoMigrate(&testRecord{}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPragmasApplyToEveryConnection(t *testing.T) {
	s := testStore(t)
	for _, pool := range []*sql.DB{s.readerPool, s.writerPool} {
		var connections []*sql.Conn
		for range pool.Stats().MaxOpenConnections {
			conn, err := pool.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			connections = append(connections, conn)
			for pragma, want := range map[string]int{"busy_timeout": 5000, "foreign_keys": 1, "synchronous": 2} {
				var got int
				if err := conn.QueryRowContext(context.Background(), "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
					t.Errorf("%s=%d, want %d, error: %v", pragma, got, want, err)
				}
			}
			var mode string
			if err := conn.QueryRowContext(context.Background(), "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
				t.Errorf("journal_mode=%s, error: %v", mode, err)
			}
		}
		for _, conn := range connections {
			_ = conn.Close()
		}
	}
}

func TestConcurrentWritesShareCommitsAndAreReadableOnReturn(t *testing.T) {
	s := testStore(t)
	const writers = 256
	start := make(chan struct{})
	var wg sync.WaitGroup
	var transactions sync.Map
	var active, peak atomic.Int32
	for i := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			err := s.write(context.Background(), func(tx *gorm.DB) error {
				current := active.Add(1)
				defer active.Add(-1)
				for previous := peak.Load(); current > previous && !peak.CompareAndSwap(previous, current); previous = peak.Load() {
				}
				transactions.Store(tx.Statement.ConnPool, true)
				return tx.Create(&testRecord{ID: id, Value: id}).Error
			})
			if err != nil {
				t.Errorf("write %d: %v", id, err)
				return
			}
			var record testRecord
			if err := s.reader.First(&record, id).Error; err != nil || record.Value != id {
				t.Errorf("read committed %d: value=%d error=%v", id, record.Value, err)
			}
		}(i + 1)
	}
	close(start)
	wg.Wait()
	if peak.Load() != 1 {
		t.Errorf("concurrent writers=%d, want 1", peak.Load())
	}
	commits := 0
	transactions.Range(func(_, _ any) bool { commits++; return true })
	if commits >= writers/2 {
		t.Errorf("%d tasks used %d commits; expected group commits", writers, commits)
	}
	stats := s.stats()
	if stats.JobsCommitted != writers || stats.JobsFailed != 0 || stats.BatchesCommitted != uint64(commits) || stats.LastBatchSize > writeBatchSize {
		t.Errorf("incorrect concurrent write statistics: %+v", stats)
	}
}

func TestImmediateWriteTransactionAndReadersDoNotSeeDirtyData(t *testing.T) {
	s := testStore(t)
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- s.write(context.Background(), func(tx *gorm.DB) error {
			close(started)
			<-release
			return tx.Create(&testRecord{ID: 1}).Error
		})
	}()
	<-started
	conn, err := s.readerPool.Conn(context.Background())
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "PRAGMA busy_timeout=20"); err != nil {
		t.Error(err)
	}
	// The writer has only read so far. BEGIN IMMEDIATE must already hold the
	// write reservation, avoiding a later read-to-write SQLITE_BUSY upgrade.
	otherTx, beginErr := conn.BeginTx(context.Background(), nil)
	if beginErr == nil {
		_ = otherTx.Rollback()
		t.Error("writer did not reserve its write lock at BEGIN")
	}
	_, _ = conn.ExecContext(context.Background(), "PRAGMA busy_timeout=5000")
	_ = conn.Close()
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}

	inserted, commit := make(chan struct{}), make(chan struct{})
	go func() {
		result <- s.write(context.Background(), func(tx *gorm.DB) error {
			if err := tx.Create(&testRecord{ID: 2}).Error; err != nil {
				close(inserted)
				return err
			}
			close(inserted)
			<-commit
			return nil
		})
	}()
	<-inserted
	var count int64
	if err := s.reader.Model(&testRecord{}).Where("id = ?", 2).Count(&count).Error; err != nil || count != 0 {
		t.Errorf("read saw uncommitted data: count=%d error=%v", count, err)
	}
	close(commit)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func batchRequest(fn func(*gorm.DB) error) *writeRequest {
	r := &writeRequest{ctx: context.Background(), fn: fn, result: make(chan error, 1)}
	r.state.Store(requestQueued)
	return r
}

func TestSavepointsIsolateFailedAndPanickingJobs(t *testing.T) {
	s := testStore(t)
	rejected := errors.New("reject entire operation")
	batch := []*writeRequest{
		batchRequest(func(tx *gorm.DB) error { return tx.Create(&testRecord{ID: 1}).Error }),
		batchRequest(func(tx *gorm.DB) error {
			if err := tx.Create(&testRecord{ID: 2}).Error; err != nil {
				return err
			}
			return rejected
		}),
		batchRequest(func(tx *gorm.DB) error {
			if err := tx.Create(&testRecord{ID: 3}).Error; err != nil {
				return err
			}
			panic("test callback")
		}),
		batchRequest(func(tx *gorm.DB) error { return tx.Create(&testRecord{ID: 4}).Error }),
	}
	s.commitBatch(batch)
	for i, request := range batch {
		err := <-request.result
		if (i == 1 || i == 2) != (err != nil) {
			t.Errorf("job %d error=%v", i, err)
		}
	}
	var ids []int
	if err := s.reader.Model(&testRecord{}).Order("id").Pluck("id", &ids).Error; err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ids) != "[1 4]" {
		t.Fatalf("partially failed jobs leaked rows: %v", ids)
	}
	stats := s.stats()
	if stats.JobsCommitted != 2 || stats.JobsFailed != 2 || stats.BatchesCommitted != 1 || stats.LastBatchSize != 4 {
		t.Errorf("incorrect savepoint statistics: %+v", stats)
	}
}

func TestFailedOuterCommitRollsBackEveryJobAndReusesConnection(t *testing.T) {
	s := testStore(t)
	for _, statement := range []string{
		"CREATE TABLE parents (id INTEGER PRIMARY KEY)",
		"CREATE TABLE children (id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES parents(id) DEFERRABLE INITIALLY DEFERRED)",
	} {
		if err := s.reader.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	batch := []*writeRequest{
		batchRequest(func(tx *gorm.DB) error { return tx.Create(&testRecord{ID: 1}).Error }),
		batchRequest(func(tx *gorm.DB) error { return tx.Exec("INSERT INTO children VALUES (1, 99)").Error }),
	}
	s.commitBatch(batch)
	for _, request := range batch {
		if err := <-request.result; err == nil {
			t.Fatal("acknowledged failed commit")
		}
	}
	stats := s.stats()
	if stats.JobsCommitted != 0 || stats.JobsFailed != 2 || stats.BatchesCommitted != 0 {
		t.Errorf("failed transaction counted as committed: %+v", stats)
	}
	var count int64
	if err := s.reader.Model(&testRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("commit failure left data: count=%d error=%v", count, err)
	}
	if err := s.write(context.Background(), func(tx *gorm.DB) error {
		return tx.Create(&testRecord{ID: 2}).Error
	}); err != nil {
		t.Fatalf("failed commit poisoned writer connection: %v", err)
	}
}

func TestSQLiteAutomaticRollbackFailsWholeBatchAndRecovers(t *testing.T) {
	s := testStore(t)
	if err := s.reader.Exec(`CREATE TRIGGER abort_transaction BEFORE INSERT ON test_records
WHEN NEW.id = 2 BEGIN SELECT RAISE(ROLLBACK, 'abort whole transaction'); END`).Error; err != nil {
		t.Fatal(err)
	}
	var lastExecuted bool
	batch := []*writeRequest{
		batchRequest(func(tx *gorm.DB) error { return tx.Create(&testRecord{ID: 1}).Error }),
		batchRequest(func(tx *gorm.DB) error { return tx.Create(&testRecord{ID: 2}).Error }),
		batchRequest(func(tx *gorm.DB) error {
			lastExecuted = true
			return tx.Create(&testRecord{ID: 3}).Error
		}),
	}
	s.commitBatch(batch)
	for _, request := range batch {
		if err := <-request.result; err == nil {
			t.Fatal("acknowledged a job from an automatically rolled back transaction")
		}
	}
	if lastExecuted {
		t.Fatal("continued executing jobs after SQLite invalidated the outer transaction")
	}
	var count int64
	if err := s.reader.Model(&testRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("automatic rollback leaked data: count=%d error=%v", count, err)
	}
	if err := s.write(context.Background(), func(tx *gorm.DB) error {
		return tx.Create(&testRecord{ID: 4}).Error
	}); err != nil {
		t.Fatalf("automatic rollback poisoned the next transaction: %v", err)
	}
}

func TestCancellationAndShutdownDrain(t *testing.T) {
	s := testStore(t)
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- s.write(ctx, func(tx *gorm.DB) error {
			close(started)
			<-release
			return tx.Create(&testRecord{ID: 1}).Error
		})
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		t.Fatalf("running write returned before final commit: %v", err)
	default:
	}
	queuedCtx, cancelQueued := context.WithCancel(context.Background())
	var executed atomic.Bool
	request, err := s.enqueue(queuedCtx, func(tx *gorm.DB) error { executed.Store(true); return nil }, false)
	if err != nil {
		t.Fatal(err)
	}
	cancelQueued()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stopCancel()
	if err := s.shutdown(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown while running: %v", err)
	}
	if err := s.write(context.Background(), func(tx *gorm.DB) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("accepted after shutdown: %v", err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := <-request.result; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued cancel: %v", err)
	}
	if executed.Load() {
		t.Fatal("canceled queued mutation executed")
	}
	if err := s.shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBoundedQueueHonorsCancellation(t *testing.T) {
	s := testStore(t)
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	go func() {
		_ = s.write(context.Background(), func(tx *gorm.DB) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	for range writeQueueSize {
		if _, err := s.enqueue(context.Background(), func(tx *gorm.DB) error { return nil }, false); err != nil {
			t.Fatal(err)
		}
	}
	if depth := s.stats().QueueDepth; depth != writeQueueSize {
		t.Errorf("queueDepth=%d, want %d", depth, writeQueueSize)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := s.write(ctx, func(tx *gorm.DB) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("full queue did not honor cancellation: %v", err)
	}
}

func TestFlushWaitsForEarlierCommit(t *testing.T) {
	s := testStore(t)
	r, err := s.enqueue(context.Background(), func(tx *gorm.DB) error {
		return tx.Create(&testRecord{ID: 1}).Error
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-r.result:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("flush completed before earlier write acknowledgement")
	}
	var count int64
	if err := s.reader.Model(&testRecord{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("flush did not commit preceding write: count=%d error=%v", count, err)
	}
	stats := s.stats()
	if stats.JobsCommitted != 1 || stats.LastBatchSize != 1 {
		t.Errorf("flush barrier counted as mutation: %+v", stats)
	}
}
