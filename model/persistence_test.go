package model

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lanthora/cacao/storage"
)

func persistenceFixture(t *testing.T) (User, Net) {
	t.Helper()
	db := storage.Get()
	u := User{Name: "tx" + strings.ReplaceAll(uuid.NewString(), "-", "")[:18], Role: "normal"}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	n := Net{UserID: u.ID, Name: "network", DHCP: "192.168.202.0/24"}
	if err := n.Create(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Unscoped().Where("net_id IN (SELECT id FROM nets WHERE user_id = ?)", u.ID).Delete(&Device{})
		db.Unscoped().Where("net_id IN (SELECT id FROM nets WHERE user_id = ?)", u.ID).Delete(&Route{})
		db.Unscoped().Where("user_id = ?", u.ID).Delete(&Net{})
		db.Unscoped().Delete(&u)
	})
	return u, n
}

func TestTreeDeletionRollbackAndRetry(t *testing.T) {
	u, n := persistenceFixture(t)
	db := storage.Get()
	d := Device{NetID: n.ID, VMac: "0123456789abcdef", RX: 7, TX: 9}
	r := Route{NetID: n.ID}
	if err := db.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.Create(); err != nil {
		t.Fatal(err)
	}
	trigger := "reject_route_delete_test"
	if err := db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE OF deleted_at ON routes WHEN OLD.net_id = %d BEGIN SELECT RAISE(ABORT, 'simulated delete failure'); END`, trigger, n.ID)).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP TRIGGER IF EXISTS " + trigger) })
	if _, err := DeleteUserTree(u.ID); err == nil {
		t.Fatal("tree deletion ignored child failure")
	}
	for _, record := range []interface{}{&u, &n, &d, &r} {
		if err := db.First(record).Error; err != nil {
			t.Fatalf("rollback lost %T: %v", record, err)
		}
	}
	if err := db.Exec("DROP TRIGGER " + trigger).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteUserTree(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteUserTree(u.ID); err != nil {
		t.Fatalf("repeated deletion failed: %v", err)
	}
	for _, entity := range []interface{}{&Net{}, &Device{}, &Route{}} {
		query := db.Model(entity)
		if _, isNet := entity.(*Net); isNet {
			query = query.Where("user_id = ?", u.ID)
		} else {
			query = query.Where("net_id = ?", n.ID)
		}
		var count int64
		if err := query.Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("orphaned %T: %d %v", entity, count, err)
		}
	}
	var historical Device
	if err := db.Unscoped().First(&historical, d.ID).Error; err != nil || historical.RX != 7 || historical.TX != 9 {
		t.Fatal("historical traffic lost")
	}
}

func TestConcurrentNaturalKeyCreatesAreIdempotent(t *testing.T) {
	u, n := persistenceFixture(t)
	var wg sync.WaitGroup
	errors := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			network := Net{UserID: u.ID, Name: n.Name, DHCP: n.DHCP}
			errors <- network.Create()
			route := Route{NetID: n.ID, DstAddr: "10.0.0.0", DstMask: "255.0.0.0"}
			errors <- route.Create()
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var networks, routes int64
	storage.Get().Model(&Net{}).Where("user_id = ?", u.ID).Count(&networks)
	storage.Get().Model(&Route{}).Where("net_id = ?", n.ID).Count(&routes)
	if networks != 1 || routes != 1 {
		t.Fatalf("duplicate natural keys: networks=%d routes=%d", networks, routes)
	}
}
