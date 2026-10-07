package main

import (
	"context"
	"time"

	"github.com/lanthora/cacao/background"
	"github.com/lanthora/cacao/candy"
	"github.com/lanthora/cacao/logger"
	"github.com/lanthora/cacao/model"
)

func newBackgroundTasks() (*background.Manager, error) {
	manager := background.New()
	maintenance := func(force bool) func(context.Context) (background.Result, error) {
		return func(ctx context.Context) (background.Result, error) {
			if _, err := candy.FlushDeviceState(ctx); err != nil {
				return background.Result{}, err
			}
			result, err := model.CleanMaintenance(ctx, model.MaintenanceOptions{Now: time.Now(), BatchSize: 250, CleanInactiveUsers: true, ForceInactiveUsers: force})
			if err == nil {
				for _, id := range result.DeletedNetIDs {
					candy.DeleteNet(id)
				}
			}
			return background.Result{RowsAffected: result.RowsAffected, Details: result.Details}, err
		}
	}
	tasks := []background.Task{
		{Name: "device-flush", Interval: 5 * time.Second, Run: func(ctx context.Context) (background.Result, error) {
			count, err := candy.FlushDeviceState(ctx)
			return background.Result{RowsAffected: int64(count), Details: map[string]int64{"pendingDevices": int64(model.PendingDeviceStateCount())}}, err
		}},
		{Name: "maintenance", Interval: time.Minute, RunOnStart: true, Run: maintenance(false)},
		{Name: "clean-inactive-users", ManualOnly: true, Run: maintenance(true)},
		{Name: "integrity-audit", Interval: 5 * time.Minute, RunOnStart: true, Run: func(ctx context.Context) (background.Result, error) {
			counts, err := model.AuditIntegrity(ctx)
			return background.Result{Details: counts}, err
		}},
	}
	for _, task := range tasks {
		name, run := task.Name, task.Run
		task.Run = func(ctx context.Context) (background.Result, error) {
			result, err := run(ctx)
			if err != nil {
				logger.Info("background task %s failed: %v", name, err)
			}
			return result, err
		}
		if err := manager.Register(task); err != nil {
			return nil, err
		}
	}
	return manager, nil
}
