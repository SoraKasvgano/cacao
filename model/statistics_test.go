package model

import "testing"

func TestStatisticsCountsDoNotMultiplyOrIncludeDeletedChildren(t *testing.T) {
	db := maintenanceTestDB(t)
	users := []User{{Name: "populated"}, {Name: "empty"}}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	nets := []Net{{UserID: users[0].ID, Name: "first"}, {UserID: users[0].ID, Name: "second"}, {UserID: users[0].ID, Name: "deleted"}}
	if err := db.Create(&nets).Error; err != nil {
		t.Fatal(err)
	}
	devices := []Device{{NetID: nets[0].ID}, {NetID: nets[0].ID}, {NetID: nets[1].ID}, {NetID: nets[2].ID}}
	if err := db.Create(&devices).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&devices[1]).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&nets[2]).Error; err != nil {
		t.Fatal(err)
	}
	var rows []UserWithStatistics
	if err := db.Raw(userStatisticsQuery + " ORDER BY users.id").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != users[0].ID || rows[0].NetNum != 2 || rows[0].DevNum != 2 || rows[1].NetNum != 0 || rows[1].DevNum != 0 {
		t.Fatalf("incorrect grouped statistics: %+v", rows)
	}
	if err := db.Delete(&users[0]).Error; err != nil {
		t.Fatal(err)
	}
	rows = nil
	if err := db.Raw(userStatisticsQuery).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != users[1].ID {
		t.Fatalf("deleted user leaked into statistics: %+v", rows)
	}
}
