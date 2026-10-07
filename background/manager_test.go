package background

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskDoesNotOverlapAndStopCancels(t *testing.T) {
	m := New()
	started := make(chan struct{})
	var calls atomic.Int32
	if err := m.Register(Task{Name: "slow", Interval: time.Millisecond, RunOnStart: true, Run: func(ctx context.Context) (Result, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-ctx.Done()
		return Result{}, ctx.Err()
	}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("task did not start")
	}
	for i := 0; i < 20; i++ {
		if err := m.Trigger("slow"); !errors.Is(err, ErrTaskRunning) {
			t.Fatalf("overlap trigger = %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("overlapping runs: %d", calls.Load())
	}
	state := m.Snapshot()[0]
	if state.Running || state.NextRunAt != nil || state.Runs != 1 || state.Failures != 1 || state.LastError == "" {
		t.Fatalf("unexpected stopped state: %+v", state)
	}
	if err := m.Trigger("slow"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("trigger after stop = %v", err)
	}
}

func TestPanicIsObservableAndTaskCanRunAgain(t *testing.T) {
	m := New()
	var calls atomic.Int32
	if err := m.Register(Task{Name: "recover", Interval: time.Hour, Run: func(context.Context) (Result, error) {
		if calls.Add(1) == 1 {
			panic("test failure")
		}
		return Result{RowsAffected: 3, Details: map[string]int64{"deleted": 3}}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	if err := m.Trigger("recover"); err != nil {
		t.Fatal(err)
	}
	waitRuns(t, m, 1)
	state := m.Snapshot()[0]
	if state.Failures != 1 || state.LastError == "" || state.LastSuccessAt != nil {
		t.Fatalf("panic not observable: %+v", state)
	}
	if err := m.Trigger("recover"); err != nil {
		t.Fatal(err)
	}
	waitRuns(t, m, 2)
	state = m.Snapshot()[0]
	if state.Failures != 1 || state.LastError != "" || state.LastSuccessAt == nil || state.TotalRowsAffected != 3 {
		t.Fatalf("recovery not observable: %+v", state)
	}
	state.Details["deleted"] = 99
	*state.NextRunAt = time.Time{}
	if next := m.Snapshot()[0]; next.Details["deleted"] != 3 || next.NextRunAt.IsZero() {
		t.Fatal("snapshot aliases internal state")
	}
}

func waitRuns(t *testing.T, manager *Manager, count uint64) {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if manager.Snapshot()[0].Runs >= count {
			return
		}
		select {
		case <-deadline:
			t.Fatal("task did not finish")
		case <-ticker.C:
		}
	}
}

func TestManualOnlyTaskNeverRunsAutomatically(t *testing.T) {
	m := New()
	ran := make(chan struct{}, 4)
	if err := m.Register(Task{Name: "manual", ManualOnly: true, Run: func(context.Context) (Result, error) {
		ran <- struct{}{}
		return Result{RowsAffected: 1}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	select {
	case <-ran:
		t.Fatal("manual-only task ran on start")
	case <-time.After(20 * time.Millisecond):
	}
	state := m.Snapshot()[0]
	if !state.ManualOnly || state.NextRunAt != nil || state.Runs != 0 {
		t.Fatalf("unexpected manual schedule: %+v", state)
	}
	if err := m.Trigger("manual"); err != nil {
		t.Fatal(err)
	}
	waitRuns(t, m, 1)
	<-ran
	select {
	case <-ran:
		t.Fatal("manual-only task repeated automatically")
	case <-time.After(20 * time.Millisecond):
	}
	if state := m.Snapshot()[0]; state.NextRunAt != nil || state.Runs != 1 {
		t.Fatalf("manual task scheduled itself: %+v", state)
	}
	if err := m.Trigger("manual"); err != nil {
		t.Fatal(err)
	}
	waitRuns(t, m, 2)
}
