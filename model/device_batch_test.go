package model

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

func newBatchTestNetwork(t *testing.T) (User, Net) {
	t.Helper()
	user := User{Name: fmt.Sprintf("batch-%d", time.Now().UnixNano()), Role: "normal"}
	network := Net{Name: "batch", Password: "secret", DHCP: "10.30.0.0/16"}
	if err := storage.Write(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		network.UserID = user.ID
		return tx.Create(&network).Error
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		devicePersistenceMutex.Lock()
		defer devicePersistenceMutex.Unlock()
		deviceStates.Lock()
		for id, entry := range deviceStates.entries {
			if entry.device.NetID == network.ID {
				removeDeviceStateLocked(id)
			}
		}
		deviceStates.Unlock()
		if err := storage.Write(func(tx *gorm.DB) error {
			if err := tx.Unscoped().Where("net_id = ?", network.ID).Delete(&Device{}).Error; err != nil {
				return err
			}
			if err := tx.Unscoped().Delete(&network).Error; err != nil {
				return err
			}
			return tx.Unscoped().Delete(&user).Error
		}); err != nil {
			t.Error(err)
		}
	})
	return user, network
}

func authenticateBatchDevice(t *testing.T, network Net, vmac, ip string) (Device, uint64) {
	t.Helper()
	device, generation, err := AuthenticateDevice(context.Background(), network.ID, network.Password, vmac, ip, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return device, generation
}

func TestDeviceTelemetryCoalescesAndOverlaysReads(t *testing.T) {
	user, network := newBatchTestNetwork(t)
	device, generation := authenticateBatchDevice(t, network, "1000000000000001", "10.30.0.1")
	for i := 1; i <= 100; i++ {
		device.RX, device.TX = uint64(i), uint64(i*2)
		device.Hostname = fmt.Sprintf("host-%d", i)
		QueueDeviceState(device, generation)
	}
	var persisted Device
	if err := storage.Get().First(&persisted, device.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.RX != 0 || persisted.Hostname != "" {
		t.Fatal("telemetry wrote through before batch flush")
	}
	if current := GetDeviceByDevID(device.ID); current.RX != 100 || current.Hostname != "host-100" {
		t.Fatalf("missing live overlay: %+v", current)
	}
	totals, err := GetUserTrafficTotals([]uint{user.ID})
	if err != nil || totals[user.ID].TX != 200 {
		t.Fatalf("pending traffic missing: %+v %v", totals, err)
	}
	if count, err := FlushDeviceStates(context.Background()); err != nil || count != 1 {
		t.Fatalf("flush = %d, %v", count, err)
	}
	if err := storage.Get().First(&persisted, device.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.RX != 100 || persisted.TX != 200 || persisted.Hostname != "host-100" {
		t.Fatalf("incomplete batched telemetry: %+v", persisted)
	}
	if count, err := FlushDeviceStates(context.Background()); err != nil || count != 0 {
		t.Fatalf("unchanged batch repeated writes: %d %v", count, err)
	}
	totals, err = GetUserTrafficTotals([]uint{user.ID})
	if err != nil || totals[user.ID].TX != 200 {
		t.Fatalf("flushed traffic double counted: %+v %v", totals, err)
	}
}

func TestDeviceBatchFailureRetainsLatestState(t *testing.T) {
	_, network := newBatchTestNetwork(t)
	device, generation := authenticateBatchDevice(t, network, "1000000000000002", "10.30.0.2")
	device.RX = 20
	QueueDeviceState(device, generation)
	trigger := fmt.Sprintf("batch_test_failure_%d", device.ID)
	if err := storage.Write(func(tx *gorm.DB) error {
		return tx.Exec(fmt.Sprintf("CREATE TRIGGER %s BEFORE UPDATE ON devices WHEN NEW.id = %d BEGIN SELECT RAISE(ABORT, 'injected telemetry failure'); END", trigger, device.ID)).Error
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		storage.Write(func(tx *gorm.DB) error { return tx.Exec("DROP TRIGGER IF EXISTS " + trigger).Error })
	})
	if _, err := FlushDeviceStates(context.Background()); err == nil {
		t.Fatal("injected write failure was swallowed")
	}
	device.RX = 30
	QueueDeviceState(device, generation)
	if err := storage.Write(func(tx *gorm.DB) error { return tx.Exec("DROP TRIGGER " + trigger).Error }); err != nil {
		t.Fatal(err)
	}
	if count, err := FlushDeviceStates(context.Background()); err != nil || count != 1 {
		t.Fatalf("retry = %d, %v", count, err)
	}
	var persisted Device
	storage.Get().First(&persisted, device.ID)
	if persisted.RX != 30 {
		t.Fatalf("retry lost latest state: %+v", persisted)
	}
}

func TestDeviceReconnectRejectsOldGenerationAndRetainsTraffic(t *testing.T) {
	_, network := newBatchTestNetwork(t)
	old, oldGeneration := authenticateBatchDevice(t, network, "1000000000000003", "10.30.0.3")
	old.RX = 33
	QueueDeviceState(old, oldGeneration)
	current, generation := authenticateBatchDevice(t, network, old.VMac, "10.30.0.4")
	if current.ID != old.ID || current.RX != 33 || generation == oldGeneration {
		t.Fatal("reconnect did not retain identity and unflushed traffic")
	}
	old.Online, old.RX = false, 999
	QueueDeviceState(old, oldGeneration)
	if got := GetDeviceByDevID(old.ID); !got.Online || got.RX != 33 || got.IP != "10.30.0.4" {
		t.Fatalf("old session replaced current state: %+v", got)
	}
	current.TX, current.Online = 44, false
	QueueDeviceState(current, generation)
	if err := current.Delete(); err != nil {
		t.Fatal(err)
	}
	QueueDeviceState(current, generation)
	if _, err := FlushDeviceStates(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := GetDeviceByDevID(current.ID); got.ID != 0 {
		t.Fatal("late telemetry resurrected deleted device")
	}
}

func TestDeviceDeleteRechecksOnlineAfterReconnect(t *testing.T) {
	_, network := newBatchTestNetwork(t)
	device, generation := authenticateBatchDevice(t, network, "1000000000000011", "10.30.0.11")
	device.Online = false
	QueueDeviceState(device, generation)
	stale := GetDeviceByDevID(device.ID)
	if stale.Online {
		t.Fatal("disconnect was not visible")
	}
	current, currentGeneration := authenticateBatchDevice(t, network, device.VMac, device.IP)
	if err := stale.Delete(); !errors.Is(err, ErrDeviceOnline) {
		t.Fatalf("stale delete removed a reconnected device: %v", err)
	}
	if err := (&Device{}).Delete(); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("zero ID delete = %v", err)
	}
	current.Online, current.TX = false, 77
	QueueDeviceState(current, currentGeneration)
	if err := stale.Delete(); err != nil {
		t.Fatalf("pending disconnect prevented deletion: %v", err)
	}
	var persisted Device
	if err := storage.Get().Unscoped().First(&persisted, device.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !persisted.DeletedAt.Valid || persisted.TX != 77 {
		t.Fatalf("delete lost final telemetry: %+v", persisted)
	}
	if err := stale.Delete(); err != nil {
		t.Fatalf("repeat deletion was not idempotent: %v", err)
	}
}

func TestDeviceAuthenticationRejectsConflictsAndMissingParents(t *testing.T) {
	_, network := newBatchTestNetwork(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := AuthenticateDevice(context.Background(), network.ID, network.Password, fmt.Sprintf("%016x", i), "10.30.0.5", "", "")
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent IP claim succeeded %d times", successes)
	}
	if err := storage.Write(func(tx *gorm.DB) error { return tx.Delete(&network).Error }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AuthenticateDevice(context.Background(), network.ID, network.Password, "1000000000000009", "10.30.0.9", "", ""); err == nil {
		t.Fatal("authentication created a child under a deleted network")
	}
}

func TestDeviceBatchSpansSQLParameterLimitAndKeepsConcurrentChanges(t *testing.T) {
	_, network := newBatchTestNetwork(t)
	const size = 65
	devices := make([]Device, size)
	generations := make([]uint64, size)
	for i := range devices {
		devices[i], generations[i] = authenticateBatchDevice(t, network, fmt.Sprintf("%016x", 100+i), fmt.Sprintf("10.30.1.%d", i+1))
		devices[i].RX = uint64(i + 1)
		QueueDeviceState(devices[i], generations[i])
	}
	done := make(chan error, 1)
	go func() { _, err := FlushDeviceStates(context.Background()); done <- err }()
	for i := range devices {
		devices[i].RX = uint64(1000 + i)
		QueueDeviceState(devices[i], generations[i])
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := FlushDeviceStates(context.Background()); err != nil {
		t.Fatal(err)
	}
	var persisted []Device
	if err := storage.Get().Where("net_id = ?", network.ID).Order("id").Find(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if len(persisted) != size {
		t.Fatalf("batch size = %d", len(persisted))
	}
	for i, device := range persisted {
		if device.RX != uint64(1000+i) {
			t.Fatalf("batch lost concurrent update at %d: %d", i, device.RX)
		}
	}
}

// Hold the writer inside an already-started transaction. Operations submitted
// afterwards cannot commit until release, just as when SQLite waits on a lock.
func holdDeviceTestWriter(t *testing.T) func() {
	t.Helper()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	var result error
	go func() {
		result = storage.Write(func(tx *gorm.DB) error { close(started); <-release; return nil })
		close(done)
	}()
	releaseWriter := func() {
		once.Do(func() { close(release) })
		select {
		case <-done:
			if result != nil {
				t.Errorf("blocking test writer failed: %v", result)
			}
		case <-time.After(3 * time.Second):
			t.Error("blocking test writer did not exit")
		}
	}
	t.Cleanup(releaseWriter)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("test writer did not start")
	}
	return releaseWriter
}

func waitDeviceTestWriteQueued(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for storage.Stats().QueueDepth == 0 {
		if time.Now().After(deadline) {
			t.Fatal("device operation did not reach the writer queue")
		}
		time.Sleep(time.Millisecond)
	}
}

func queueDeviceWhileWriterBlocked(t *testing.T, device Device, generation uint64) {
	t.Helper()
	done := make(chan struct{})
	go func() { QueueDeviceState(device, generation); close(done) }()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("telemetry was blocked by an unrelated pending SQLite transaction")
	}
}

func TestDeviceAuthenticationDoesNotBlockTelemetryDuringSlowCommit(t *testing.T) {
	user, network := newBatchTestNetwork(t)
	_, otherNetwork := newBatchTestNetwork(t)
	old, oldGeneration := authenticateBatchDevice(t, network, "1000000000000021", "10.30.0.21")
	other, otherGeneration := authenticateBatchDevice(t, otherNetwork, "1000000000000022", "10.30.0.22")
	old.RX, old.TX = 10, 20
	QueueDeviceState(old, oldGeneration)
	releaseWriter := holdDeviceTestWriter(t)
	type authResult struct {
		device     Device
		generation uint64
		err        error
	}
	authenticated := make(chan authResult, 1)
	go func() {
		device, generation, err := AuthenticateDevice(context.Background(), network.ID, network.Password, old.VMac, old.IP, "new-country", "new-region")
		authenticated <- authResult{device: device, generation: generation, err: err}
	}()
	waitDeviceTestWriteQueued(t)
	other.RX = 90
	queueDeviceWhileWriterBlocked(t, other, otherGeneration)
	old.RX, old.TX, old.Hostname, old.Online = 70, 80, "late-ping", false
	queueDeviceWhileWriterBlocked(t, old, oldGeneration)
	releaseWriter()
	result := <-authenticated
	if result.err != nil {
		t.Fatal(result.err)
	}
	current := result.device
	if current.RX != 70 || current.TX != 80 || current.Hostname != "late-ping" || !current.Online || current.Country != "new-country" || result.generation == oldGeneration {
		t.Fatalf("reconnect lost telemetry received during commit: %+v", current)
	}
	var persisted Device
	if err := storage.Get().First(&persisted, current.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.RX != 10 || persisted.TX != 20 {
		t.Fatalf("unexpected committed baseline: %+v", persisted)
	}
	totals, err := GetUserTrafficTotals([]uint{user.ID})
	if err != nil || totals[user.ID].RX != 70 || totals[user.ID].TX != 80 {
		t.Fatalf("pending reconnect delta was lost or double counted: %+v, %v", totals, err)
	}
	current.RX, current.TX = 75, 85
	QueueDeviceState(current, result.generation)
	old.RX, old.TX = 999, 999
	QueueDeviceState(old, oldGeneration)
	if _, err := FlushDeviceStates(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := storage.Get().First(&persisted, current.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.RX != 75 || persisted.TX != 85 {
		t.Fatalf("new generation counters were overwritten: %+v", persisted)
	}
	totals, err = GetUserTrafficTotals([]uint{user.ID})
	if err != nil || totals[user.ID].RX != 75 || totals[user.ID].TX != 85 {
		t.Fatalf("flushed reconnect delta was double counted: %+v, %v", totals, err)
	}
}

func TestDeviceDeleteDoesNotBlockOtherTelemetryDuringSlowCommit(t *testing.T) {
	_, network := newBatchTestNetwork(t)
	_, otherNetwork := newBatchTestNetwork(t)
	device, generation := authenticateBatchDevice(t, network, "1000000000000031", "10.30.0.31")
	other, otherGeneration := authenticateBatchDevice(t, otherNetwork, "1000000000000032", "10.30.0.32")
	device.Online, device.TX = false, 40
	QueueDeviceState(device, generation)
	releaseWriter := holdDeviceTestWriter(t)
	deleted := make(chan error, 1)
	go func() { deleted <- device.Delete() }()
	waitDeviceTestWriteQueued(t)
	other.TX = 123
	queueDeviceWhileWriterBlocked(t, other, otherGeneration)
	releaseWriter()
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	if _, err := FlushDeviceStates(context.Background()); err != nil {
		t.Fatal(err)
	}
	var persisted Device
	if err := storage.Get().First(&persisted, other.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.TX != 123 {
		t.Fatalf("independent telemetry did not persist: %+v", persisted)
	}
	persisted = Device{}
	if err := storage.Get().Unscoped().First(&persisted, device.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !persisted.DeletedAt.Valid || persisted.TX != 40 {
		t.Fatalf("delete did not preserve its captured offline state: %+v", persisted)
	}
}

func TestDeletingDeviceTreesPreservesPendingCountersWithinScope(t *testing.T) {
	for _, scope := range []string{"network", "user"} {
		t.Run(scope, func(t *testing.T) {
			user, network := newBatchTestNetwork(t)
			sibling := Net{UserID: user.ID, Name: "sibling", Password: "secret", DHCP: "10.31.0.0/16"}
			if err := storage.Write(func(tx *gorm.DB) error { return tx.Create(&sibling).Error }); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				storage.Write(func(tx *gorm.DB) error {
					if err := tx.Unscoped().Where("net_id = ?", sibling.ID).Delete(&Device{}).Error; err != nil {
						return err
					}
					return tx.Unscoped().Delete(&sibling).Error
				})
				deviceStates.Lock()
				for id, entry := range deviceStates.entries {
					if entry.device.NetID == sibling.ID {
						removeDeviceStateLocked(id)
					}
				}
				deviceStates.Unlock()
			})
			device, generation := authenticateBatchDevice(t, network, "1000000000000041", "10.30.0.41")
			other, otherGeneration := authenticateBatchDevice(t, sibling, "1000000000000042", "10.31.0.42")
			device.RX, device.TX = 41, 82
			other.RX, other.TX = 99, 198
			QueueDeviceState(device, generation)
			QueueDeviceState(other, otherGeneration)
			netID := network.ID
			if scope == "user" {
				netID = 0
			}
			if err := WriteDeletingDevices(context.Background(), user.ID, netID, func(tx *gorm.DB) error {
				if netID != 0 {
					if err := tx.Where("net_id = ?", netID).Delete(&Device{}).Error; err != nil {
						return err
					}
					return tx.Delete(&network).Error
				}
				if err := tx.Where("net_id IN (SELECT id FROM nets WHERE user_id = ?)", user.ID).Delete(&Device{}).Error; err != nil {
					return err
				}
				if err := tx.Where("user_id = ?", user.ID).Delete(&Net{}).Error; err != nil {
					return err
				}
				return tx.Delete(&user).Error
			}); err != nil {
				t.Fatal(err)
			}
			var persisted Device
			if err := storage.Get().Unscoped().First(&persisted, device.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !persisted.DeletedAt.Valid || persisted.RX != 41 || persisted.TX != 82 {
				t.Fatalf("tree deletion lost pending counters: %+v", persisted)
			}
			persisted = Device{}
			if err := storage.Get().Unscoped().First(&persisted, other.ID).Error; err != nil {
				t.Fatal(err)
			}
			if scope == "network" {
				if persisted.DeletedAt.Valid || persisted.RX != 0 || GetDeviceByDevID(other.ID).RX != 99 {
					t.Fatalf("network deletion consumed sibling telemetry: %+v", persisted)
				}
			} else if !persisted.DeletedAt.Valid || persisted.RX != 99 {
				t.Fatalf("user deletion did not save all networks: %+v", persisted)
			}
			device.RX = 999
			QueueDeviceState(device, generation)
			if _, err := FlushDeviceStates(context.Background()); err != nil {
				t.Fatal(err)
			}
			persisted = Device{}
			if err := storage.Get().Unscoped().First(&persisted, device.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !persisted.DeletedAt.Valid || persisted.RX != 41 {
				t.Fatal("old generation changed a deleted tree")
			}
		})
	}
}

func TestDeletingDeviceTreeFailureRetainsDirtyStateAndRollsBack(t *testing.T) {
	user, network := newBatchTestNetwork(t)
	device, generation := authenticateBatchDevice(t, network, "1000000000000051", "10.30.0.51")
	device.RX = 51
	QueueDeviceState(device, generation)
	injected := errors.New("injected tree deletion failure")
	err := WriteDeletingDevices(context.Background(), user.ID, network.ID, func(tx *gorm.DB) error {
		if err := tx.Delete(&network).Error; err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("tree deletion error = %v", err)
	}
	var persisted Device
	if err := storage.Get().First(&persisted, device.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.RX != 0 || GetDeviceByDevID(device.ID).RX != 51 {
		t.Fatal("failed deletion committed counters or dropped dirty state")
	}
	if _, err := FlushDeviceStates(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := storage.Get().First(&persisted, device.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.RX != 51 {
		t.Fatal("failed deletion could not retry its pending telemetry")
	}
	if err := WriteDeletingDevices(context.Background(), 0, 0, func(tx *gorm.DB) error { return nil }); err == nil {
		t.Fatal("unbounded deletion scope was accepted")
	}
}
