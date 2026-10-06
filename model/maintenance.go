package model

import (
	"context"
	"strconv"
	"time"

	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

type MaintenanceOptions struct {
	Now                time.Time
	BatchSize          int
	CleanInactiveUsers bool
	// ForceInactiveUsers is for an explicit administrator request. Automatic
	// runs always honor autoCleanUser; both paths honor the configured threshold.
	ForceInactiveUsers bool
}

type MaintenanceResult struct {
	RowsAffected  int64
	DeletedNetIDs []uint
	Details       map[string]int64
}

// CleanMaintenance performs one bounded transaction. Children are removed before
// parents; a large subtree is consumed across runs without creating new orphans.
// All deletion is soft deletion: historical traffic remains available.
// Callers flush buffered device activity before calling and remove DeletedNetIDs
// from the in-memory network registry only after this transaction commits.
func CleanMaintenance(ctx context.Context, options MaintenanceOptions) (MaintenanceResult, error) {
	return cleanMaintenance(ctx, options, func(fn func(*gorm.DB) error) error { return storage.WriteContext(ctx, fn) })
}

func cleanMaintenance(ctx context.Context, options MaintenanceOptions, write func(func(*gorm.DB) error) error) (MaintenanceResult, error) {
	result := MaintenanceResult{Details: make(map[string]int64)}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	if options.BatchSize <= 0 {
		options.BatchSize = 250
	}
	if options.BatchSize > 1000 {
		options.BatchSize = 1000
	}
	err := write(func(tx *gorm.DB) error {
		tx = tx.WithContext(ctx)
		var config []Config
		if err := tx.Where("key IN ?", []string{"autoCleanUser", "inactiveUserThreshold"}).Order("id DESC").Find(&config).Error; err != nil {
			return err
		}
		settings := make(map[string]string)
		for _, item := range config {
			if _, found := settings[item.Key]; !found {
				settings[item.Key] = item.Value
			}
		}
		var inactiveUsers []uint
		if options.CleanInactiveUsers && (options.ForceInactiveUsers || settings["autoCleanUser"] == "true") {
			threshold, err := strconv.Atoi(settings["inactiveUserThreshold"])
			if err != nil || threshold <= 0 {
				threshold = 7
			}
			// An excessively large legacy threshold means no account is old enough.
			if threshold <= 3650000 {
				if err := tx.Model(&User{}).Where("role = ? AND updated_at < ?", "normal", options.Now.AddDate(0, 0, -threshold)).
					Where(`NOT EXISTS (SELECT 1 FROM nets JOIN devices ON devices.net_id = nets.id
					 WHERE nets.user_id = users.id AND nets.deleted_at IS NULL
					 AND devices.deleted_at IS NULL AND devices.online = ?)`, true).
					Order("id").Limit(options.BatchSize).Pluck("id", &inactiveUsers).Error; err != nil {
					return err
				}
			}
		}
		// These predicates also repair children of hard-deleted legacy parents.
		invalidNet := `NOT EXISTS (SELECT 1 FROM nets JOIN users ON users.id = nets.user_id
		 WHERE nets.id = devices.net_id AND nets.deleted_at IS NULL AND users.deleted_at IS NULL)`
		devices := tx.Model(&Device{}).Where(invalidNet).
			Or(`online = ? AND EXISTS (SELECT 1 FROM nets JOIN users ON users.id = nets.user_id
			 WHERE nets.id = devices.net_id AND nets.deleted_at IS NULL AND users.deleted_at IS NULL
			 AND nets.lease > 0 AND julianday(devices.updated_at) + nets.lease < julianday(?))`, false, options.Now)
		if len(inactiveUsers) > 0 {
			devices = devices.Or("net_id IN (SELECT id FROM nets WHERE user_id IN ?)", inactiveUsers)
		}
		if err := maintenanceDeleteBatch(tx, devices, &Device{}, options.BatchSize, "devices", &result); err != nil {
			return err
		}

		routes := tx.Model(&Route{}).Where(`NOT EXISTS (SELECT 1 FROM nets JOIN users ON users.id = nets.user_id
		 WHERE nets.id = routes.net_id AND nets.deleted_at IS NULL AND users.deleted_at IS NULL)`)
		if len(inactiveUsers) > 0 {
			routes = routes.Or("net_id IN (SELECT id FROM nets WHERE user_id IN ?)", inactiveUsers)
		}
		if err := maintenanceDeleteBatch(tx, routes, &Route{}, options.BatchSize, "routes", &result); err != nil {
			return err
		}

		nets := tx.Model(&Net{}).Where("NOT EXISTS (SELECT 1 FROM users WHERE users.id = nets.user_id AND users.deleted_at IS NULL)")
		if len(inactiveUsers) > 0 {
			nets = nets.Or("user_id IN ?", inactiveUsers)
		}
		// Group the parent predicate before applying child-existence guards.
		var netIDs []uint
		if err := tx.Model(&Net{}).Where(nets).
			Where("NOT EXISTS (SELECT 1 FROM devices WHERE devices.net_id = nets.id AND devices.deleted_at IS NULL)").
			Where("NOT EXISTS (SELECT 1 FROM routes WHERE routes.net_id = nets.id AND routes.deleted_at IS NULL)").
			Order("id").Limit(options.BatchSize).Pluck("id", &netIDs).Error; err != nil {
			return err
		}
		if len(netIDs) > 0 {
			deleted := tx.Where("id IN ?", netIDs).Delete(&Net{})
			if deleted.Error != nil {
				return deleted.Error
			}
			result.RowsAffected += deleted.RowsAffected
			result.Details["nets"] = deleted.RowsAffected
			result.DeletedNetIDs = netIDs
		}
		if len(inactiveUsers) > 0 {
			users := tx.Model(&User{}).Where("id IN ?", inactiveUsers).
				Where("NOT EXISTS (SELECT 1 FROM nets WHERE nets.user_id = users.id AND nets.deleted_at IS NULL)")
			if err := maintenanceDeleteBatch(tx, users, &User{}, options.BatchSize, "users", &result); err != nil {
				return err
			}
		}
		var sessionIDs []uint
		if err := tx.Model(&User{}).Where("token <> ? AND token_expires_at IS NOT NULL AND token_expires_at <= ?", "", options.Now).
			Order("id").Limit(options.BatchSize).Pluck("id", &sessionIDs).Error; err != nil {
			return err
		}
		if len(sessionIDs) > 0 {
			cleared := tx.Model(&User{}).Where("id IN ?", sessionIDs).
				UpdateColumns(map[string]interface{}{"token": "", "token_expires_at": nil})
			if cleared.Error != nil {
				return cleared.Error
			}
			result.RowsAffected += cleared.RowsAffected
			result.Details["expiredSessions"] = cleared.RowsAffected
		}
		return nil
	})
	if err != nil {
		return MaintenanceResult{Details: make(map[string]int64)}, err
	}
	return result, nil
}

func maintenanceDeleteBatch(tx, candidates *gorm.DB, entity interface{}, limit int, name string, result *MaintenanceResult) error {
	var ids []uint
	if err := candidates.Order("id").Limit(limit).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	deleted := tx.Where("id IN ?", ids).Delete(entity)
	if deleted.Error != nil {
		return deleted.Error
	}
	result.RowsAffected += deleted.RowsAffected
	result.Details[name] += deleted.RowsAffected
	return nil
}

// AuditIntegrity only reports ambiguous legacy identities. It never guesses
// which account/network/device/config should be deleted to resolve a conflict.
func AuditIntegrity(ctx context.Context) (map[string]int64, error) {
	return auditIntegrity(storage.Get().WithContext(ctx))
}

func auditIntegrity(db *gorm.DB) (map[string]int64, error) {
	checks := map[string]string{
		"duplicateUsers":     "SELECT COUNT(*) FROM (SELECT name FROM users WHERE deleted_at IS NULL GROUP BY name HAVING COUNT(*) > 1)",
		"duplicateNets":      "SELECT COUNT(*) FROM (SELECT user_id, name FROM nets WHERE deleted_at IS NULL GROUP BY user_id, name HAVING COUNT(*) > 1)",
		"duplicateDevices":   "SELECT COUNT(*) FROM (SELECT net_id, vmac FROM devices WHERE deleted_at IS NULL GROUP BY net_id, vmac HAVING COUNT(*) > 1)",
		"duplicateDeviceIPs": "SELECT COUNT(*) FROM (SELECT net_id, ip FROM devices WHERE deleted_at IS NULL AND ip <> '' GROUP BY net_id, ip HAVING COUNT(*) > 1)",
		"duplicateRoutes":    "SELECT COUNT(*) FROM (SELECT net_id, dev_addr, dev_mask, dst_addr, dst_mask, next_hop, priority FROM routes WHERE deleted_at IS NULL GROUP BY net_id, dev_addr, dev_mask, dst_addr, dst_mask, next_hop, priority HAVING COUNT(*) > 1)",
		"duplicateConfigs":   "SELECT COUNT(*) FROM (SELECT key FROM configs WHERE deleted_at IS NULL GROUP BY key HAVING COUNT(*) > 1)",
		"orphanNets":         "SELECT COUNT(*) FROM nets WHERE deleted_at IS NULL AND NOT EXISTS (SELECT 1 FROM users WHERE users.id = nets.user_id AND users.deleted_at IS NULL)",
		"orphanDevices":      "SELECT COUNT(*) FROM devices WHERE deleted_at IS NULL AND NOT EXISTS (SELECT 1 FROM nets JOIN users ON users.id = nets.user_id WHERE nets.id = devices.net_id AND nets.deleted_at IS NULL AND users.deleted_at IS NULL)",
		"orphanRoutes":       "SELECT COUNT(*) FROM routes WHERE deleted_at IS NULL AND NOT EXISTS (SELECT 1 FROM nets JOIN users ON users.id = nets.user_id WHERE nets.id = routes.net_id AND nets.deleted_at IS NULL AND users.deleted_at IS NULL)",
	}
	result := make(map[string]int64, len(checks))
	for name, query := range checks {
		var count int64
		if err := db.Raw(query).Scan(&count).Error; err != nil {
			return nil, err
		}
		result[name] = count
	}
	return result, nil
}
