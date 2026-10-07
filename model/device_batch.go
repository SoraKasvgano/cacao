package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

// Persistence operations share an order, while telemetry can keep advancing
// during a flush. A successful flush only acknowledges the captured version.
var devicePersistenceMutex sync.Mutex
var deviceStates = struct {
	sync.Mutex
	sequence   uint64
	entries    map[uint]deviceStateEntry
	identities map[deviceIdentity]uint
}{entries: make(map[uint]deviceStateEntry), identities: make(map[deviceIdentity]uint)}

type deviceIdentity struct {
	netID uint
	vmac  string
}

func removeDeviceStateLocked(id uint) {
	if entry, ok := deviceStates.entries[id]; ok {
		identity := deviceIdentity{netID: entry.device.NetID, vmac: entry.device.VMac}
		if deviceStates.identities[identity] == id {
			delete(deviceStates.identities, identity)
		}
	}
	delete(deviceStates.entries, id)
}

type deviceStateEntry struct {
	device      Device
	userID      uint
	generation  uint64
	version     uint64
	dirty       bool
	persistedRX uint64
	persistedTX uint64
}

// PendingDeviceStateCount is the number of coalesced device updates waiting
// for a successful flush, including retries after a database error.
func PendingDeviceStateCount() int {
	deviceStates.Lock()
	defer deviceStates.Unlock()
	count := 0
	for _, entry := range deviceStates.entries {
		if entry.dirty {
			count++
		}
	}
	return count
}

// AuthenticateDevice durably reserves the identity and address before the
// websocket is authorized. Reconnects reuse counters, including queued traffic.
func AuthenticateDevice(ctx context.Context, netID uint, password, vmac, ip, country, region string) (Device, uint64, error) {
	devicePersistenceMutex.Lock()
	defer devicePersistenceMutex.Unlock()
	deviceStates.Lock()
	identity := deviceIdentity{netID: netID, vmac: vmac}
	buffered, hasBuffered := deviceStates.entries[deviceStates.identities[identity]]
	deviceStates.Unlock()
	var device Device
	var ownerID uint
	err := storage.WriteContext(ctx, func(tx *gorm.DB) error {
		var parent Net
		if err := tx.Model(&Net{}).Joins("JOIN users ON users.id = nets.user_id AND users.deleted_at IS NULL").Where("nets.id = ? AND nets.password = ?", netID, password).Take(&parent).Error; err != nil {
			return fmt.Errorf("device network is unavailable: %w", err)
		}
		ownerID = parent.UserID
		err := tx.Where("net_id = ? AND vmac = ?", netID, vmac).Take(&device).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var conflicts int64
		if err := tx.Model(&Device{}).Where("net_id = ? AND ip = ? AND id <> ?", netID, ip, device.ID).Count(&conflicts).Error; err != nil {
			return err
		}
		if conflicts > 0 {
			return fmt.Errorf("device IP address is already reserved")
		}
		if hasBuffered && buffered.device.ID == device.ID {
			copyDeviceTelemetry(&device, buffered.device)
		}
		device.NetID, device.VMac, device.IP = netID, vmac, ip
		device.Online, device.Country, device.Region = true, country, region
		if device.ID == 0 {
			if err := tx.Create(&device).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Model(&device).Select("ip", "online", "rx", "tx", "os", "version", "hostname", "country", "region").Updates(&device).Error; err != nil {
				return err
			}
		}
		return tx.Model(&User{}).Where("id = ?", ownerID).UpdateColumn("updated_at", device.UpdatedAt).Error
	})
	if err != nil {
		return Device{}, 0, err
	}
	persistedRX, persistedTX := device.RX, device.TX
	dirty := false
	deviceStates.Lock()
	defer deviceStates.Unlock()
	if latest, ok := deviceStates.entries[device.ID]; hasBuffered && ok && latest.generation == buffered.generation && latest.version != buffered.version {
		// Packets and pings keep advancing while SQLite commits. Transfer the
		// latest telemetry to the new session, retaining the actual committed
		// counters as its baseline so the next flush writes only pending state.
		online, authCountry, authRegion, committedAt := device.Online, device.Country, device.Region, device.UpdatedAt
		copyDeviceTelemetry(&device, latest.device)
		device.Online, device.Country, device.Region = online, authCountry, authRegion
		if device.UpdatedAt.Before(committedAt) {
			device.UpdatedAt = committedAt
		}
		dirty = true
	}
	deviceStates.sequence++
	generation := deviceStates.sequence
	deviceStates.entries[device.ID] = deviceStateEntry{device: device, userID: ownerID, generation: generation, version: generation, dirty: dirty, persistedRX: persistedRX, persistedTX: persistedTX}
	deviceStates.identities[identity] = device.ID
	return device, generation, nil
}

// QueueDeviceState never creates a database row. The generation rejects late
// callbacks from a retired socket after reconnect, deletion or password change.
func QueueDeviceState(device Device, generation uint64) {
	if device.ID == 0 || generation == 0 {
		return
	}
	deviceStates.Lock()
	defer deviceStates.Unlock()
	entry, exists := deviceStates.entries[device.ID]
	if !exists || entry.generation != generation {
		return
	}
	deviceStates.sequence++
	entry.version, entry.dirty = deviceStates.sequence, true
	copyDeviceTelemetry(&entry.device, device)
	entry.device.UpdatedAt = time.Now()
	deviceStates.entries[device.ID] = entry
}

func copyDeviceTelemetry(dst *Device, src Device) {
	dst.Online, dst.RX, dst.TX = src.Online, src.RX, src.TX
	dst.OS, dst.Version, dst.Hostname = src.OS, src.Version, src.Hostname
	dst.Country, dst.Region = src.Country, src.Region
	dst.UpdatedAt = src.UpdatedAt
}

func OverlayDeviceState(device *Device) {
	deviceStates.Lock()
	defer deviceStates.Unlock()
	if entry, ok := deviceStates.entries[device.ID]; ok {
		copyDeviceTelemetry(device, entry.device)
	}
}

// Only rows already returned by the database receive an overlay. Deleted rows
// are never reintroduced into API results by queued telemetry.
func OverlayDeviceStates(devices []Device) {
	deviceStates.Lock()
	defer deviceStates.Unlock()
	for i := range devices {
		if entry, ok := deviceStates.entries[devices[i].ID]; ok {
			copyDeviceTelemetry(&devices[i], entry.device)
		}
	}
}

func writeDeviceSnapshotsTx(tx *gorm.DB, pending []deviceStateEntry, applied map[uint]struct{}) error {
	for start := 0; start < len(pending); start += 48 {
		end := start + 48
		if end > len(pending) {
			end = len(pending)
		}
		batch := pending[start:end]
		var query strings.Builder
		args := make([]interface{}, 0, len(batch)*19)
		query.WriteString("UPDATE devices SET ")
		columns := []string{"online", "rx", "tx", "os", "version", "hostname", "country", "region", "updated_at"}
		for colIndex, col := range columns {
			if colIndex > 0 {
				query.WriteString(", ")
			}
			query.WriteString(col + " = CASE id")
			for _, entry := range batch {
				d := entry.device
				values := []interface{}{d.Online, d.RX, d.TX, d.OS, d.Version, d.Hostname, d.Country, d.Region, d.UpdatedAt}
				query.WriteString(" WHEN ? THEN ?")
				args = append(args, d.ID, values[colIndex])
			}
			query.WriteString(" END")
		}
		ids := make([]uint, 0, len(batch))
		for _, entry := range batch {
			ids = append(ids, entry.device.ID)
		}
		query.WriteString(" WHERE id IN ? AND deleted_at IS NULL AND EXISTS (SELECT 1 FROM nets JOIN users ON users.id = nets.user_id AND users.deleted_at IS NULL WHERE nets.id = devices.net_id AND nets.deleted_at IS NULL) RETURNING id")
		args = append(args, ids)
		var updatedIDs []uint
		if err := tx.Raw(query.String(), args...).Scan(&updatedIDs).Error; err != nil {
			return err
		}
		for _, id := range updatedIDs {
			applied[id] = struct{}{}
		}
		if len(updatedIDs) > 0 {
			now := time.Now()
			if err := tx.Exec("UPDATE users SET updated_at = CASE WHEN updated_at < ? THEN ? ELSE updated_at END WHERE deleted_at IS NULL AND id IN (SELECT nets.user_id FROM nets JOIN devices ON devices.net_id = nets.id WHERE devices.id IN ? AND nets.deleted_at IS NULL AND devices.deleted_at IS NULL)", now, now, updatedIDs).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// FlushDeviceStates writes all captured telemetry in one logical transaction,
// with at most 48 devices per statement to stay below SQLite's parameter limit.
func FlushDeviceStates(ctx context.Context) (int, error) {
	devicePersistenceMutex.Lock()
	defer devicePersistenceMutex.Unlock()
	deviceStates.Lock()
	pending := make([]deviceStateEntry, 0, len(deviceStates.entries))
	for _, entry := range deviceStates.entries {
		if entry.dirty {
			pending = append(pending, entry)
		}
	}
	deviceStates.Unlock()
	if len(pending) == 0 {
		return 0, nil
	}
	applied := make(map[uint]struct{}, len(pending))
	err := storage.WriteContext(ctx, func(tx *gorm.DB) error {
		return writeDeviceSnapshotsTx(tx, pending, applied)
	})
	if err != nil {
		return 0, err
	}
	deviceStates.Lock()
	defer deviceStates.Unlock()
	for _, captured := range pending {
		entry, ok := deviceStates.entries[captured.device.ID]
		if !ok || entry.generation != captured.generation {
			continue
		}
		if _, exists := applied[captured.device.ID]; !exists {
			removeDeviceStateLocked(captured.device.ID)
			continue
		}
		entry.persistedRX, entry.persistedTX = captured.device.RX, captured.device.TX
		if entry.version == captured.version {
			if !entry.device.Online {
				removeDeviceStateLocked(captured.device.ID)
				continue
			}
			entry.dirty = false
		}
		deviceStates.entries[captured.device.ID] = entry
	}
	return len(applied), nil
}

// WriteDeletingDevices saves the currently buffered telemetry and deletes a
// business tree in one logical transaction. Capturing is the counter cutoff:
// packets already being handled may arrive later, but cannot revive the rows.
// The mutation callback must only use tx and must not acquire application locks.
// A nonzero netID restricts the scope to that network; userID can further limit
// its owner. Without netID, userID selects all of that user's tracked devices.
func WriteDeletingDevices(ctx context.Context, userID, netID uint, mutation func(*gorm.DB) error) error {
	if (userID == 0 && netID == 0) || mutation == nil {
		return fmt.Errorf("invalid device tree deletion scope")
	}
	devicePersistenceMutex.Lock()
	defer devicePersistenceMutex.Unlock()
	deviceStates.Lock()
	captured := make([]deviceStateEntry, 0)
	pending := make([]deviceStateEntry, 0)
	for _, entry := range deviceStates.entries {
		if netID != 0 && entry.device.NetID != netID {
			continue
		}
		if userID != 0 && entry.userID != userID {
			continue
		}
		captured = append(captured, entry)
		if entry.dirty {
			pending = append(pending, entry)
		}
	}
	deviceStates.Unlock()
	if err := storage.WriteContext(ctx, func(tx *gorm.DB) error {
		if err := writeDeviceSnapshotsTx(tx, pending, make(map[uint]struct{}, len(pending))); err != nil {
			return err
		}
		return mutation(tx)
	}); err != nil {
		return err
	}
	deviceStates.Lock()
	for _, snapshot := range captured {
		if latest, ok := deviceStates.entries[snapshot.device.ID]; ok && latest.generation == snapshot.generation {
			removeDeviceStateLocked(snapshot.device.ID)
		}
	}
	deviceStates.Unlock()
	return nil
}

type DeviceTraffic struct {
	RX uint64
	TX uint64
}

// GetUserTrafficTotals combines one grouped query with unflushed deltas. Taking
// the persistence lock avoids double-counting a flush between those two steps.
func GetUserTrafficTotals(userIDs []uint) (map[uint]DeviceTraffic, error) {
	devicePersistenceMutex.Lock()
	defer devicePersistenceMutex.Unlock()
	var rows []struct {
		UserID uint
		RX     uint64
		TX     uint64
	}
	query := storage.Get().Unscoped().Model(&Device{}).Select("nets.user_id, COALESCE(SUM(devices.rx), 0) AS rx, COALESCE(SUM(devices.tx), 0) AS tx").Joins("JOIN nets ON devices.net_id = nets.id").Group("nets.user_id")
	if len(userIDs) > 0 {
		query = query.Where("nets.user_id IN ?", userIDs)
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	totals := make(map[uint]DeviceTraffic, len(rows))
	for _, row := range rows {
		totals[row.UserID] = DeviceTraffic{RX: row.RX, TX: row.TX}
	}
	selected := make(map[uint]struct{}, len(userIDs))
	for _, id := range userIDs {
		selected[id] = struct{}{}
	}
	deviceStates.Lock()
	defer deviceStates.Unlock()
	for _, entry := range deviceStates.entries {
		if len(selected) > 0 {
			if _, ok := selected[entry.userID]; !ok {
				continue
			}
		}
		total := totals[entry.userID]
		if entry.device.RX >= entry.persistedRX {
			total.RX += entry.device.RX - entry.persistedRX
		}
		if entry.device.TX >= entry.persistedTX {
			total.TX += entry.device.TX - entry.persistedTX
		}
		totals[entry.userID] = total
	}
	return totals, nil
}
