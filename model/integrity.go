package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

var ErrConflict = errors.New("active record already exists")

// EnsureIntegrity installs safeguards without deleting or merging ambiguous
// legacy identities. Maintenance reports existing duplicates for operator review.
func EnsureIntegrity() error {
	return storage.Write(installIntegrity)
}

func installIntegrity(tx *gorm.DB) error {
	for _, item := range []struct{ table, keys, name string }{
		{"users", "name", "ux_active_users_name"},
		{"nets", "user_id, name", "ux_active_nets_name"},
		{"devices", "net_id, vmac", "ux_active_devices_vmac"},
		{"configs", "key", "ux_active_configs_key"},
		{"routes", "net_id, dev_addr, dev_mask, dst_addr, dst_mask, next_hop, priority", "ux_active_routes_entry"},
	} {
		var duplicates int64
		if err := tx.Raw(fmt.Sprintf("SELECT COUNT(*) FROM (SELECT 1 FROM %s WHERE deleted_at IS NULL GROUP BY %s HAVING COUNT(*) > 1)", item.table, item.keys)).Scan(&duplicates).Error; err != nil {
			return err
		}
		if duplicates == 0 {
			if err := tx.Exec(fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s) WHERE deleted_at IS NULL", item.name, item.table, item.keys)).Error; err != nil {
				return err
			}
		}
		// The trigger also protects new writes when legacy duplicates prevent
		// installing a unique index. Identifiers here are compile-time constants.
		var comparisons []string
		var changes []string
		for _, key := range strings.Split(item.keys, ", ") {
			comparisons = append(comparisons, key+" = NEW."+key)
			changes = append(changes, "NEW."+key+" IS NOT OLD."+key)
		}
		condition := strings.Join(comparisons, " AND ")
		for _, operation := range []struct{ name, sql string }{{"insert", "INSERT"}, {"update", "UPDATE OF " + item.keys + ", deleted_at"}} {
			changed := ""
			if operation.name == "update" {
				changed = " AND (OLD.deleted_at IS NOT NULL OR " + strings.Join(changes, " OR ") + ")"
			}
			if err := tx.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS guard_%s_%s", item.table, operation.name)).Error; err != nil {
				return err
			}
			query := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS guard_%s_%s BEFORE %s ON %s
WHEN NEW.deleted_at IS NULL%s AND EXISTS (SELECT 1 FROM %s WHERE deleted_at IS NULL AND id <> NEW.id AND %s)
BEGIN SELECT RAISE(ABORT, 'duplicate active record'); END`, item.table, operation.name, operation.sql, item.table, changed, item.table, condition)
			if err := tx.Exec(query).Error; err != nil {
				return err
			}
		}
	}
	for _, child := range []struct{ table, parent, fk string }{{"nets", "users", "user_id"}, {"devices", "nets", "net_id"}, {"routes", "nets", "net_id"}} {
		parentQuery := fmt.Sprintf("SELECT 1 FROM %s WHERE id = NEW.%s AND deleted_at IS NULL", child.parent, child.fk)
		if child.parent == "nets" {
			parentQuery = "SELECT 1 FROM nets JOIN users ON users.id = nets.user_id AND users.deleted_at IS NULL WHERE nets.id = NEW.net_id AND nets.deleted_at IS NULL"
		}
		for _, operation := range []struct{ name, sql string }{{"insert", "INSERT"}, {"update", "UPDATE OF " + child.fk + ", deleted_at"}} {
			if err := tx.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS guard_%s_parent_%s", child.table, operation.name)).Error; err != nil {
				return err
			}
			query := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS guard_%s_parent_%s BEFORE %s ON %s
WHEN NEW.deleted_at IS NULL AND NOT EXISTS (%s)
BEGIN SELECT RAISE(ABORT, 'inactive or missing parent'); END`, child.table, operation.name, operation.sql, child.table, parentQuery)
			if err := tx.Exec(query).Error; err != nil {
				return err
			}
		}
		query := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS cascade_%s_soft_delete AFTER UPDATE OF deleted_at ON %s
WHEN NEW.deleted_at IS NOT NULL
BEGIN UPDATE %s SET deleted_at = NEW.deleted_at WHERE %s = NEW.id AND deleted_at IS NULL; END`, child.table, child.parent, child.table, child.fk)
		if err := tx.Exec(query).Error; err != nil {
			return err
		}
		// Preserve historical children as tombstones even if an operator
		// physically deletes a parent outside the application.
		query = fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS cascade_%s_delete AFTER DELETE ON %s
BEGIN UPDATE %s SET deleted_at = CURRENT_TIMESTAMP WHERE %s = OLD.id AND deleted_at IS NULL; END`, child.table, child.parent, child.table, child.fk)
		if err := tx.Exec(query).Error; err != nil {
			return err
		}
	}
	var duplicateIPs int64
	if err := tx.Raw("SELECT COUNT(*) FROM (SELECT 1 FROM devices WHERE deleted_at IS NULL AND ip <> '' GROUP BY net_id, ip HAVING COUNT(*) > 1)").Scan(&duplicateIPs).Error; err != nil {
		return err
	}
	if duplicateIPs == 0 {
		if err := tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS ux_active_devices_ip ON devices(net_id, ip) WHERE deleted_at IS NULL AND ip <> ''").Error; err != nil {
			return err
		}
	}
	for _, operation := range []struct{ name, sql string }{{"insert", "INSERT"}, {"update", "UPDATE OF net_id, ip, deleted_at"}} {
		changed := ""
		if operation.name == "update" {
			changed = " AND (OLD.deleted_at IS NOT NULL OR NEW.net_id IS NOT OLD.net_id OR NEW.ip IS NOT OLD.ip)"
		}
		if err := tx.Exec("DROP TRIGGER IF EXISTS guard_devices_ip_" + operation.name).Error; err != nil {
			return err
		}
		if err := tx.Exec(fmt.Sprintf(`CREATE TRIGGER guard_devices_ip_%s BEFORE %s ON devices
WHEN NEW.deleted_at IS NULL AND NEW.ip <> ''%s AND EXISTS (SELECT 1 FROM devices WHERE id <> NEW.id AND deleted_at IS NULL AND net_id = NEW.net_id AND ip = NEW.ip)
BEGIN SELECT RAISE(ABORT, 'duplicate active device IP'); END`, operation.name, operation.sql, changed)).Error; err != nil {
			return err
		}
	}
	return tx.Exec("CREATE INDEX IF NOT EXISTS idx_devices_net_online ON devices (net_id, online, deleted_at)").Error
}

// DeleteUserTree is atomic and safe to repeat. Network connections are revoked
// by the caller only after this transaction has committed.
func DeleteUserTree(userID uint) ([]uint, error) {
	if userID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var netIDs []uint
	err := WriteDeletingDevices(context.Background(), userID, 0, func(tx *gorm.DB) error {
		if err := tx.Unscoped().Model(&Net{}).Where("user_id = ?", userID).Pluck("id", &netIDs).Error; err != nil {
			return err
		}
		if len(netIDs) > 0 {
			// Subqueries avoid SQLite's parameter limit for accounts with many nets.
			nets := tx.Unscoped().Model(&Net{}).Select("id").Where("user_id = ?", userID)
			if err := tx.Where("net_id IN (?)", nets).Delete(&Device{}).Error; err != nil {
				return err
			}
			if err := tx.Where("net_id IN (?)", nets).Delete(&Route{}).Error; err != nil {
				return err
			}
			if err := tx.Where("user_id = ?", userID).Delete(&Net{}).Error; err != nil {
				return err
			}
		}
		return tx.Where("id = ?", userID).Delete(&User{}).Error
	})
	return netIDs, err
}

func DeleteNetworkTree(netID, ownerID uint) error {
	if netID == 0 || ownerID == 0 {
		return gorm.ErrRecordNotFound
	}
	return WriteDeletingDevices(context.Background(), ownerID, netID, func(tx *gorm.DB) error {
		var network Net
		if err := tx.Unscoped().Where("id = ? AND user_id = ?", netID, ownerID).Take(&network).Error; err != nil {
			return err
		}
		if err := tx.Where("net_id = ?", netID).Delete(&Device{}).Error; err != nil {
			return err
		}
		if err := tx.Where("net_id = ?", netID).Delete(&Route{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", netID).Delete(&Net{}).Error
	})
}

// SetConfigTx is usable inside a larger business transaction. Unchanged
// settings perform no UPDATE, so repeated requests do not generate WAL traffic.
func SetConfigTx(tx *gorm.DB, key, value string) error {
	var current Config
	err := tx.Where("key = ?", key).Order("id").Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&Config{Key: key, Value: value}).Error
	}
	if err != nil {
		return err
	}
	if current.Value == value {
		return nil
	}
	return tx.Model(&Config{}).Where("id = ?", current.ID).Updates(map[string]interface{}{"value": value, "updated_at": time.Now()}).Error
}
