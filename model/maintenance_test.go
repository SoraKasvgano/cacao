package model

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func maintenanceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "maintenance.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Config{}, &User{}, &Net{}, &Device{}, &Route{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestMaintenanceHonorsPoliciesAndPreservesTraffic(t *testing.T) {
	db := maintenanceTestDB(t)
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -30)
	users := []User{{Name: "inactive", Role: "normal", Model: gorm.Model{UpdatedAt: old}}, {Name: "online", Role: "normal", Model: gorm.Model{UpdatedAt: old}}, {Name: "admin", Role: "admin", Model: gorm.Model{UpdatedAt: old}}}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	nets := []Net{{UserID: users[0].ID, Name: "expired", Lease: 7}, {UserID: users[1].ID, Name: "online", Lease: 7}, {UserID: users[2].ID, Name: "no-lease"}}
	if err := db.Create(&nets).Error; err != nil {
		t.Fatal(err)
	}
	devices := []Device{{NetID: nets[0].ID, VMac: "offline", RX: 123, TX: 456, Model: gorm.Model{UpdatedAt: old}}, {NetID: nets[1].ID, VMac: "online", Online: true, Model: gorm.Model{UpdatedAt: old}}, {NetID: nets[2].ID, VMac: "no-lease", Model: gorm.Model{UpdatedAt: old}}}
	if err := db.Create(&devices).Error; err != nil {
		t.Fatal(err)
	}
	route := Route{NetID: nets[0].ID}
	if err := db.Create(&route).Error; err != nil {
		t.Fatal(err)
	}
	run := func(options MaintenanceOptions) MaintenanceResult {
		t.Helper()
		options.Now = now
		result, err := cleanMaintenance(context.Background(), options, func(fn func(*gorm.DB) error) error { return db.Transaction(fn) })
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := run(MaintenanceOptions{CleanInactiveUsers: true})
	if result.Details["devices"] != 1 || result.Details["users"] != 0 {
		t.Fatalf("automatic policy not honored: %+v", result)
	}
	var historical Device
	if err := db.Unscoped().First(&historical, devices[0].ID).Error; err != nil || historical.RX != 123 || historical.TX != 456 || !historical.DeletedAt.Valid {
		t.Fatalf("lost historical counters: %+v, %v", historical, err)
	}
	result = run(MaintenanceOptions{CleanInactiveUsers: true, ForceInactiveUsers: true})
	if result.Details["users"] != 1 || result.Details["nets"] != 1 || result.Details["routes"] != 1 {
		t.Fatalf("inactive cascade failed: %+v", result)
	}
	var remaining []User
	if err := db.Find(&remaining).Error; err != nil || len(remaining) != 2 {
		t.Fatalf("online user/admin removed: %+v %v", remaining, err)
	}
	var activeDevices int64
	db.Model(&Device{}).Count(&activeDevices)
	if activeDevices != 2 {
		t.Fatalf("online or unlimited lease device removed: %d", activeDevices)
	}
	if result := run(MaintenanceOptions{CleanInactiveUsers: true, ForceInactiveUsers: true}); result.RowsAffected != 0 {
		t.Fatalf("cleanup is not idempotent: %+v", result)
	}
}

func TestMaintenanceBoundsSubtreesAndRepairsOrphans(t *testing.T) {
	db := maintenanceTestDB(t)
	user := User{Name: "old", Role: "normal", Model: gorm.Model{UpdatedAt: time.Now().AddDate(0, 0, -30)}}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	network := Net{UserID: user.ID}
	if err := db.Create(&network).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := db.Create(&Device{NetID: network.ID, RX: 10}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		result, err := cleanMaintenance(context.Background(), MaintenanceOptions{BatchSize: 1, CleanInactiveUsers: true, ForceInactiveUsers: true}, func(fn func(*gorm.DB) error) error { return db.Transaction(fn) })
		if err != nil {
			t.Fatal(err)
		}
		if result.Details["devices"] != 1 {
			t.Fatalf("batch limit ignored: %+v", result)
		}
		var orphans int64
		db.Raw("SELECT COUNT(*) FROM devices LEFT JOIN nets ON nets.id = devices.net_id WHERE devices.deleted_at IS NULL AND (nets.id IS NULL OR nets.deleted_at IS NOT NULL)").Scan(&orphans)
		if orphans != 0 {
			t.Fatal("cleanup introduced orphans")
		}
		if i < 2 && result.Details["users"] != 0 {
			t.Fatal("parent deleted before bounded children cleanup")
		}
	}
	if err := db.Create(&Device{NetID: 99999, Online: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&Route{NetID: 99999}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := cleanMaintenance(context.Background(), MaintenanceOptions{BatchSize: 1}, func(fn func(*gorm.DB) error) error { return db.Transaction(fn) })
	if err != nil || result.Details["devices"] != 1 || result.Details["routes"] != 1 {
		t.Fatalf("orphan repair failed: %+v %v", result, err)
	}
}

func TestMaintenanceRollsBackOnCancellation(t *testing.T) {
	db := maintenanceTestDB(t)
	if err := db.Create(&Device{NetID: 99999}).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := cleanMaintenance(ctx, MaintenanceOptions{}, func(fn func(*gorm.DB) error) error { return db.Transaction(fn) })
	if err == nil || result.RowsAffected != 0 || len(result.DeletedNetIDs) != 0 {
		t.Fatalf("canceled transaction reported success: %+v %v", result, err)
	}
	var count int64
	db.Model(&Device{}).Count(&count)
	if count != 1 {
		t.Fatal("canceled cleanup deleted data")
	}
}

func TestMaintenanceRespectsThresholdAndClearsOnlyExpiredSessions(t *testing.T) {
	db := maintenanceTestDB(t)
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -30)
	expired, future := now.Add(-time.Hour), now.Add(time.Hour)
	users := []User{
		{Name: "old-normal", Role: "normal", Model: gorm.Model{UpdatedAt: old}},
		{Name: "expired-admin", Role: "admin", Token: "expired", TokenExpiresAt: &expired, Model: gorm.Model{UpdatedAt: old}},
		{Name: "live-admin", Role: "admin", Token: "live", TokenExpiresAt: &future},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	configs := []Config{{Key: "autoCleanUser", Value: "true"}, {Key: "inactiveUserThreshold", Value: "60"}}
	if err := db.Create(&configs).Error; err != nil {
		t.Fatal(err)
	}
	options := MaintenanceOptions{Now: now, CleanInactiveUsers: true}
	write := func(fn func(*gorm.DB) error) error { return db.Transaction(fn) }
	result, err := cleanMaintenance(context.Background(), options, write)
	if err != nil || result.Details["users"] != 0 || result.Details["expiredSessions"] != 1 {
		t.Fatalf("threshold/session policy failed: %+v %v", result, err)
	}
	var stale, live User
	if err := db.First(&stale, users[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if stale.Token != "" || stale.TokenExpiresAt != nil || !stale.UpdatedAt.Equal(old) {
		t.Fatalf("expired token not cleared without changing activity: %+v", stale)
	}
	if err := db.First(&live, users[2].ID).Error; err != nil || live.Token != "live" {
		t.Fatalf("live token changed: %+v %v", live, err)
	}
	if err := db.Model(&Config{}).Where("key = ?", "inactiveUserThreshold").Update("value", "7").Error; err != nil {
		t.Fatal(err)
	}
	result, err = cleanMaintenance(context.Background(), options, write)
	if err != nil || result.Details["users"] != 1 {
		t.Fatalf("enabled auto-clean did not honor changed threshold: %+v %v", result, err)
	}
}

func TestIntegrityAuditReportsDeviceIPAndRouteConflictsWithoutDeleting(t *testing.T) {
	db := maintenanceTestDB(t)
	devices := []Device{
		{NetID: 1, VMac: "a", IP: "192.0.2.1"}, {NetID: 1, VMac: "b", IP: "192.0.2.1"},
		{NetID: 1, VMac: "c"}, {NetID: 1, VMac: "d"},
		{NetID: 1, VMac: "e", IP: "192.0.2.2"}, {NetID: 1, VMac: "f", IP: "192.0.2.2"},
		{NetID: 2, VMac: "g", IP: "192.0.2.1"},
	}
	if err := db.Create(&devices).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&devices[5]).Error; err != nil {
		t.Fatal(err)
	}
	routes := []Route{
		{NetID: 1, DevAddr: "a", Priority: 1}, {NetID: 1, DevAddr: "a", Priority: 1},
		{NetID: 1, DevAddr: "a", Priority: 2}, {NetID: 1, DevAddr: "a", Priority: 2},
		{NetID: 2, DevAddr: "a", Priority: 1},
	}
	if err := db.Create(&routes).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&routes[3]).Error; err != nil {
		t.Fatal(err)
	}
	result, err := auditIntegrity(db)
	if err != nil {
		t.Fatal(err)
	}
	if result["duplicateDeviceIPs"] != 1 || result["duplicateRoutes"] != 1 {
		t.Fatalf("incorrect conflict group counts: %+v", result)
	}
	var remaining int64
	db.Model(&Device{}).Count(&remaining)
	if remaining != 6 {
		t.Fatal("audit changed device records")
	}
	db.Model(&Route{}).Count(&remaining)
	if remaining != 4 {
		t.Fatal("audit changed route records")
	}
}
