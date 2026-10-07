package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lanthora/cacao/candy"
	"github.com/lanthora/cacao/model"
	"github.com/lanthora/cacao/storage"
)

func TestAccountCreationRollsBackDefaultNetworkFailure(t *testing.T) {
	router := securityRouter()
	_, cookies := testUser(t, "admin")
	name := "atomic" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	db := storage.Get()
	trigger := "reject_default_network_test"
	if err := db.Exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON nets WHEN NEW.user_id IN (SELECT id FROM users WHERE name = '%s') BEGIN SELECT RAISE(ABORT, 'simulated network failure'); END`, trigger, name)).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec("DROP TRIGGER IF EXISTS " + trigger)
		var user model.User
		if db.Where("name = ?", name).Take(&user).Error == nil {
			var nets []model.Net
			db.Unscoped().Where("user_id = ?", user.ID).Find(&nets)
			for _, network := range nets {
				candy.DeleteNet(network.ID)
			}
			db.Unscoped().Where("user_id = ?", user.ID).Delete(&model.Net{})
			db.Unscoped().Delete(&user)
		}
	})
	body := fmt.Sprintf(`{"username":%q,"password":"test-password"}`, name)
	if _, status := securityRequest(t, router, "/api/admin/addUser", body, cookies...); status != Unexpected {
		t.Fatalf("failed commit reported success: %d", status)
	}
	var count int64
	if err := db.Unscoped().Model(&model.User{}).Where("name = ?", name).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed default network left account behind: %d %v", count, err)
	}
	if err := db.Exec("DROP TRIGGER " + trigger).Error; err != nil {
		t.Fatal(err)
	}
	if _, status := securityRequest(t, router, "/api/admin/addUser", body, cookies...); status != Success {
		t.Fatalf("retry after rollback failed: %d", status)
	}
	if _, status := securityRequest(t, router, "/api/admin/addUser", body, cookies...); status != UsernameAlreadyTaken {
		t.Fatalf("duplicate account not rejected: %d", status)
	}
	if err := db.Model(&model.Net{}).Joins("JOIN users ON users.id = nets.user_id").Where("users.name = ?", name).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("retry created duplicate/orphan networks: %d %v", count, err)
	}
}
