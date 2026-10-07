// Package background owns the lifecycle and observable state of recurring work.
package background

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

var (
	ErrNotRunning  = errors.New("background manager is not running")
	ErrTaskRunning = errors.New("background task is already running")
	ErrUnknownTask = errors.New("unknown background task")
)

type Result struct {
	RowsAffected int64            `json:"rowsAffected"`
	Details      map[string]int64 `json:"details,omitempty"`
}

type Task struct {
	Name       string
	Interval   time.Duration
	RunOnStart bool
	ManualOnly bool
	Run        func(context.Context) (Result, error)
}

type Status struct {
	Name              string           `json:"name"`
	IntervalSeconds   int64            `json:"intervalSeconds"`
	ManualOnly        bool             `json:"manualOnly"`
	Running           bool             `json:"running"`
	LastStartedAt     *time.Time       `json:"lastStartedAt"`
	LastFinishedAt    *time.Time       `json:"lastFinishedAt"`
	LastSuccessAt     *time.Time       `json:"lastSuccessAt"`
	NextRunAt         *time.Time       `json:"nextRunAt"`
	LastError         string           `json:"lastError"`
	LastDurationMs    int64            `json:"lastDurationMs"`
	LastRowsAffected  int64            `json:"lastRowsAffected"`
	TotalRowsAffected int64            `json:"totalRowsAffected"`
	Runs              uint64           `json:"runs"`
	Failures          uint64           `json:"failures"`
	Details           map[string]int64 `json:"details,omitempty"`
}

type entry struct {
	task  Task
	state Status
	wake  chan struct{}
}

type Manager struct {
	mu       sync.Mutex
	tasks    map[string]*entry
	ctx      context.Context
	cancel   context.CancelFunc
	started  bool
	stopping bool
	wg       sync.WaitGroup
}

func New() *Manager { return &Manager{tasks: make(map[string]*entry)} }

// Register must be called before Start. Names also identify manual triggers.
func (m *Manager) Register(task Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("cannot register after background manager start")
	}
	if task.Name == "" || task.Run == nil || task.Interval < 0 || (!task.ManualOnly && task.Interval == 0) || (task.ManualOnly && task.RunOnStart) {
		return errors.New("invalid background task")
	}
	if _, ok := m.tasks[task.Name]; ok {
		return fmt.Errorf("duplicate background task: %s", task.Name)
	}
	m.tasks[task.Name] = &entry{task: task, state: Status{Name: task.Name, IntervalSeconds: int64(task.Interval / time.Second), ManualOnly: task.ManualOnly}, wake: make(chan struct{}, 1)}
	return nil
}

func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("background manager already started")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.started = true
	for _, task := range m.tasks {
		if !task.task.ManualOnly {
			next := time.Now().Add(task.task.Interval)
			if task.task.RunOnStart {
				next = time.Now()
			}
			task.state.NextRunAt = &next
		}
		m.wg.Add(1)
		go m.loop(task)
	}
	return nil
}

// Stop cancels all tasks and waits for in-flight work up to the caller's deadline.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return nil
	}
	m.stopping = true
	m.cancel()
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Trigger schedules one run without blocking an HTTP request. Concurrent manual
// and scheduled triggers are coalesced; each task has exactly one worker.
func (m *Manager) Trigger(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started || m.stopping || m.ctx.Err() != nil {
		return ErrNotRunning
	}
	task, ok := m.tasks[name]
	if !ok {
		return ErrUnknownTask
	}
	if task.state.Running || len(task.wake) > 0 {
		return ErrTaskRunning
	}
	task.wake <- struct{}{}
	return nil
}

func (m *Manager) Snapshot() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]Status, 0, len(m.tasks))
	for _, task := range m.tasks {
		state := task.state
		state.Details = copyDetails(state.Details)
		state.LastStartedAt = copyTime(state.LastStartedAt)
		state.LastFinishedAt = copyTime(state.LastFinishedAt)
		state.LastSuccessAt = copyTime(state.LastSuccessAt)
		state.NextRunAt = copyTime(state.NextRunAt)
		result = append(result, state)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (m *Manager) loop(task *entry) {
	defer m.wg.Done()
	defer func() { m.mu.Lock(); task.state.NextRunAt = nil; m.mu.Unlock() }()
	delay := task.task.Interval
	if task.task.RunOnStart {
		delay = 0
	}
	var timer *time.Timer
	var timerC <-chan time.Time
	if !task.task.ManualOnly {
		timer = time.NewTimer(delay)
		timerC = timer.C
		defer timer.Stop()
	}
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-timerC:
		case <-task.wake:
		}
		m.mu.Lock()
		if m.ctx.Err() != nil {
			m.mu.Unlock()
			return
		}
		// Drain a simultaneous manual trigger so a scheduled run satisfies it.
		select {
		case <-task.wake:
		default:
		}
		task.state.Running = true
		task.state.NextRunAt = nil
		started := time.Now()
		task.state.LastStartedAt = &started
		m.mu.Unlock()
		result, err := run(m.ctx, task.task.Run)
		finished := time.Now()
		m.mu.Lock()
		task.state.Running = false
		task.state.Runs++
		task.state.LastFinishedAt = &finished
		task.state.LastDurationMs = finished.Sub(started).Milliseconds()
		task.state.LastRowsAffected = result.RowsAffected
		task.state.TotalRowsAffected += result.RowsAffected
		task.state.Details = copyDetails(result.Details)
		task.state.LastError = ""
		if err != nil {
			task.state.Failures++
			task.state.LastError = err.Error()
		} else {
			task.state.LastSuccessAt = &finished
		}
		if !task.task.ManualOnly {
			next := finished.Add(task.task.Interval)
			task.state.NextRunAt = &next
		}
		m.mu.Unlock()
		// Reset from completion; slow tasks cannot create an overdue-run storm.
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(task.task.Interval)
		}
	}
}

func run(ctx context.Context, fn func(context.Context) (Result, error)) (result Result, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("background task panic: %v", v)
		}
	}()
	return fn(ctx)
}

func copyDetails(source map[string]int64) map[string]int64 {
	if source == nil {
		return nil
	}
	result := make(map[string]int64, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
