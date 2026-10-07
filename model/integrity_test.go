package model

import (
	"testing"

	"gorm.io/gorm"
)

func TestIntegrityGuardsAndCascadePreserveTombstones(t *testing.T) {
	db := maintenanceTestDB(t)
	if err := db.Transaction(installIntegrity); err != nil {
		t.Fatal(err)
	}
	// Re-running the migration must preserve the data and refresh our guards.
	if err := db.Transaction(installIntegrity); err != nil {
		t.Fatal(err)
	}
	u := User{Name: "owner", Role: "normal"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	n := Net{UserID: u.ID, Name: "network"}
	if err := db.Create(&n).Error; err != nil {
		t.Fatal(err)
	}
	d := Device{NetID: n.ID, VMac: "0123456789abcdef", IP: "192.168.1.2", RX: 123, TX: 456}
	if err := db.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	r := Route{NetID: n.ID, DstAddr: "10.0.0.0", DstMask: "255.0.0.0"}
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	for _, duplicate := range []interface{}{
		&User{Name: u.Name}, &Net{UserID: u.ID, Name: n.Name},
		&Device{NetID: n.ID, VMac: d.VMac, IP: "192.168.1.3"},
		&Device{NetID: n.ID, VMac: "fedcba9876543210", IP: d.IP},
		&Route{NetID: n.ID, DstAddr: r.DstAddr, DstMask: r.DstMask},
	} {
		if err := db.Create(duplicate).Error; err == nil {
			t.Fatalf("duplicate accepted: %T", duplicate)
		}
	}
	for _, orphan := range []interface{}{&Net{UserID: u.ID + 100, Name: "orphan"}, &Device{NetID: n.ID + 100}, &Route{NetID: n.ID + 100}} {
		if err := db.Create(orphan).Error; err == nil {
			t.Fatalf("orphan accepted: %T", orphan)
		}
	}
	if err := db.Delete(&u).Error; err != nil {
		t.Fatal(err)
	}
	for _, entity := range []interface{}{&Net{}, &Device{}, &Route{}} {
		var count int64
		if err := db.Model(entity).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("cascade left active %T: %d %v", entity, count, err)
		}
	}
	var historical Device
	if err := db.Unscoped().First(&historical, d.ID).Error; err != nil || historical.RX != 123 || historical.TX != 456 {
		t.Fatalf("historical counters lost: %+v %v", historical, err)
	}
	if err := db.Model(&Net{}).Unscoped().Where("id = ?", n.ID).Update("deleted_at", nil).Error; err == nil {
		t.Fatal("restored network under deleted owner")
	}
	if err := db.Create(&User{Name: u.Name}).Error; err != nil {
		t.Fatalf("soft-deleted identity cannot be reused: %v", err)
	}
}

func TestLegacyDuplicatesKeepOrdinaryUpdatesButRejectNewConflicts(t *testing.T) {
	db := maintenanceTestDB(t)
	users := []User{{Name: "legacy"}, {Name: "legacy"}}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	nets := []Net{{UserID: users[0].ID, Name: "legacy"}, {UserID: users[0].ID, Name: "legacy"}}
	if err := db.Create(&nets).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(installIntegrity); err != nil {
		t.Fatalf("legacy migration blocked startup: %v", err)
	}
	if err := db.Model(&users[0]).Updates(map[string]interface{}{"name": "legacy", "password": "updated"}).Error; err != nil {
		t.Fatalf("unchanged legacy identity blocked password update: %v", err)
	}
	if err := db.Model(&nets[0]).Updates(map[string]interface{}{"name": "legacy", "password": "updated"}).Error; err != nil {
		t.Fatalf("unchanged network name blocked update: %v", err)
	}
	if err := db.Create(&User{Name: "legacy"}).Error; err == nil {
		t.Fatal("new duplicate escaped fallback trigger")
	}
	if err := db.Create(&Net{UserID: users[0].ID, Name: "legacy"}).Error; err == nil {
		t.Fatal("new network duplicate escaped fallback trigger")
	}
	var count int64
	if err := db.Model(&User{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("migration removed ambiguous accounts")
	}
}

func TestLegacyOrphanAncestorCannotGainChildren(t *testing.T) {
	db := maintenanceTestDB(t)
	n := Net{UserID: 987654, Name: "orphan"}
	if err := db.Create(&n).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(installIntegrity); err != nil {
		t.Fatal(err)
	}
	for _, child := range []interface{}{&Device{NetID: n.ID}, &Route{NetID: n.ID}} {
		if err := db.Create(child).Error; err == nil {
			t.Fatalf("invalid ancestor accepted for %T", child)
		}
	}
	// Cleanup must still be able to tombstone the legacy orphan.
	if err := db.Delete(&n).Error; err != nil {
		t.Fatal(err)
	}
}

func TestConfigRepeatedValueDoesNotWriteAgain(t *testing.T) {
	db := maintenanceTestDB(t)
	if err := db.Transaction(func(tx *gorm.DB) error { return SetConfigTx(tx, "policy", "on") }); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER forbid_config_update BEFORE UPDATE ON configs BEGIN SELECT RAISE(ABORT, 'unexpected update'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return SetConfigTx(tx, "policy", "on") }); err != nil {
		t.Fatalf("unchanged value caused a write: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return SetConfigTx(tx, "policy", "off") }); err == nil {
		t.Fatal("test trigger was not effective")
	}
}
