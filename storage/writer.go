package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	writeQueueSize = 1024
	writeBatchSize = 64
	writeBatchWait = 5 * time.Millisecond
	readPoolSize   = 8
)

const (
	requestQueued = iota
	requestRunning
	requestCanceled
)

var ErrClosed = errors.New("storage is shutting down")

type writeRequest struct {
	ctx     context.Context
	fn      func(*gorm.DB) error
	result  chan error
	state   atomic.Int32
	barrier bool
}

type store struct {
	reader, writer         *gorm.DB
	readerPool, writerPool *sql.DB
	queue                  chan *writeRequest
	slots                  chan struct{}
	stopping, done         chan struct{}
	mu                     sync.Mutex
	closed                 bool
	closeErr               error
	batchesCommitted       atomic.Uint64
	jobsCommitted          atomic.Uint64
	jobsFailed             atomic.Uint64
	lastBatchSize          atomic.Uint64
}

func (s *store) stats() Statistics {
	return Statistics{
		QueueDepth:       len(s.queue),
		BatchesCommitted: s.batchesCommitted.Load(),
		JobsCommitted:    s.jobsCommitted.Load(),
		JobsFailed:       s.jobsFailed.Load(),
		LastBatchSize:    s.lastBatchSize.Load(),
	}
}

func openDatabase(filename string) (*store, error) {
	abs, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath // Windows drive letters need file:///C:/...
	}
	params := url.Values{"_txlock": {"immediate"}, "_pragma": {
		"busy_timeout(5000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(FULL)",
	}}
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: params.Encode()}).String()
	openPool := func(size int) (*gorm.DB, *sql.DB, error) {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
			Logger: gormlogger.Default.LogMode(gormlogger.Silent),
		})
		if err != nil {
			return nil, nil, err
		}
		pool, err := db.DB()
		if err != nil {
			return nil, nil, err
		}
		pool.SetMaxOpenConns(size)
		pool.SetMaxIdleConns(size)
		return db, pool, nil
	}
	writer, writerPool, err := openPool(1)
	if err != nil {
		return nil, err
	}
	reader, readerPool, err := openPool(readPoolSize)
	if err != nil {
		_ = writerPool.Close()
		return nil, err
	}
	s := &store{
		reader: reader, writer: writer, readerPool: readerPool, writerPool: writerPool,
		queue: make(chan *writeRequest, writeQueueSize), slots: make(chan struct{}, writeQueueSize),
		stopping: make(chan struct{}), done: make(chan struct{}),
	}
	go s.run()
	return s, nil
}

func (s *store) enqueue(ctx context.Context, fn func(*gorm.DB) error, barrier bool) (*writeRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.stopping:
		return nil, ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		<-s.slots
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		<-s.slots
		return nil, err
	}
	r := &writeRequest{ctx: ctx, fn: fn, result: make(chan error, 1), barrier: barrier}
	r.state.Store(requestQueued)
	// A reserved slot guarantees this cannot block while holding the lock.
	s.queue <- r
	return r, nil
}

func (s *store) write(ctx context.Context, fn func(*gorm.DB) error) error {
	if fn == nil {
		return errors.New("storage write callback is nil")
	}
	r, err := s.enqueue(ctx, fn, false)
	if err != nil {
		return err
	}
	select {
	case err := <-r.result:
		return err
	case <-ctx.Done():
		if r.state.CompareAndSwap(requestQueued, requestCanceled) {
			return ctx.Err()
		}
		return <-r.result
	}
}

func (s *store) flush(ctx context.Context) error {
	r, err := s.enqueue(ctx, nil, true)
	if err != nil {
		return err
	}
	select {
	case err := <-r.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *store) shutdown(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.stopping)
		close(s.queue)
	}
	s.mu.Unlock()
	select {
	case <-s.done:
		return s.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *store) run() {
	defer func() {
		s.closeErr = errors.Join(s.writerPool.Close(), s.readerPool.Close())
		close(s.done)
	}()
	for first := range s.queue {
		<-s.slots
		batch := []*writeRequest{first}
		if !first.barrier {
			timer := time.NewTimer(writeBatchWait)
		collect:
			for len(batch) < writeBatchSize {
				select {
				case r, ok := <-s.queue:
					if !ok {
						break collect
					}
					<-s.slots
					batch = append(batch, r)
					if r.barrier {
						break collect
					}
				case <-timer.C:
					break collect
				}
			}
			timer.Stop()
		}
		s.commitBatch(batch)
	}
}

func invokeWrite(fn func(*gorm.DB) error, tx *gorm.DB) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("storage write callback panicked: %v", recovered)
		}
	}()
	return fn(tx)
}

func (s *store) commitBatch(batch []*writeRequest) {
	results := make([]error, len(batch))
	var tx *gorm.DB
	var conn *sql.Conn
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	var outerErr error
	var batchSize uint64
	for i, r := range batch {
		if r.barrier {
			continue
		}
		if err := r.ctx.Err(); err != nil {
			r.state.CompareAndSwap(requestQueued, requestCanceled)
		}
		if !r.state.CompareAndSwap(requestQueued, requestRunning) {
			results[i] = r.ctx.Err()
			continue
		}
		batchSize++
		if outerErr != nil {
			continue
		}
		if tx == nil {
			conn, outerErr = s.writerPool.Conn(context.Background())
			if outerErr != nil {
				continue
			}
			connectionDB := s.writer.Session(&gorm.Session{NewDB: true, SkipDefaultTransaction: true, Context: context.Background()})
			connectionDB.Statement.ConnPool = conn
			tx = connectionDB.Begin()
			if tx.Error != nil {
				outerErr = tx.Error
				continue
			}
		}
		name := fmt.Sprintf("storage_write_%d", i)
		if err := tx.Exec("SAVEPOINT " + name).Error; err != nil {
			outerErr = err
			continue
		}
		callbackDB := tx.Session(&gorm.Session{NewDB: true, SkipDefaultTransaction: true, Context: context.WithoutCancel(r.ctx)})
		results[i] = invokeWrite(r.fn, callbackDB)
		if results[i] != nil {
			if err := tx.Exec("ROLLBACK TO SAVEPOINT " + name).Error; err != nil {
				outerErr = err
				continue
			}
		}
		if err := tx.Exec("RELEASE SAVEPOINT " + name).Error; err != nil {
			outerErr = err
		}
	}
	if tx != nil {
		if outerErr == nil {
			outerErr = tx.Commit().Error
		}
		if outerErr != nil {
			// database/sql marks a Tx done even when driver.Commit fails. This
			// driver can leave the SQLite transaction open (e.g. a deferred FK
			// violation), so explicitly clean the same pinned connection.
			if err := tx.Rollback().Error; err != nil {
				if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
					// Never return a connection with uncertain transaction state
					// to the pool after an I/O or rollback failure.
					_ = conn.Raw(func(any) error { return driver.ErrBadConn })
				}
			}
		} else {
			s.batchesCommitted.Add(1)
		}
	}
	if batchSize != 0 {
		s.lastBatchSize.Store(batchSize)
	}
	for i, r := range batch {
		if outerErr != nil {
			results[i] = errors.Join(results[i], fmt.Errorf("storage batch transaction failed: %w", outerErr))
		}
		if !r.barrier {
			if results[i] == nil {
				s.jobsCommitted.Add(1)
			} else {
				s.jobsFailed.Add(1)
			}
		}
		r.result <- results[i]
	}
}
