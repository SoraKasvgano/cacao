package model

import "github.com/lanthora/cacao/storage"

type UserStatistics struct {
	NetNum uint   `json:"netnum"`
	DevNum uint   `json:"devnum"`
	RxSum  uint64 `json:"rxsum"`
	TxSum  uint64 `json:"txsum"`
}

type UserWithStatistics struct {
	User           `gorm:"embedded"`
	UserStatistics `gorm:"embedded"`
}

// Counts and traffic each use one aggregation, independent of user count.
// The traffic helper also overlays pending batched counter updates.
const userStatisticsQuery = `
SELECT users.*, COALESCE(net_stats.net_num, 0) AS net_num,
 COALESCE(device_stats.dev_num, 0) AS dev_num
FROM users
LEFT JOIN (
 SELECT user_id, COUNT(*) AS net_num FROM nets
 WHERE deleted_at IS NULL GROUP BY user_id
) AS net_stats ON net_stats.user_id = users.id
LEFT JOIN (
 SELECT nets.user_id, COUNT(*) AS dev_num
 FROM devices JOIN nets ON devices.net_id = nets.id
 WHERE devices.deleted_at IS NULL AND nets.deleted_at IS NULL GROUP BY nets.user_id
) AS device_stats ON device_stats.user_id = users.id
WHERE users.deleted_at IS NULL`

func GetUserStatistics(userid uint) (UserStatistics, error) {
	var result UserWithStatistics
	if userid == 0 {
		return result.UserStatistics, nil
	}
	if err := storage.Get().Raw(userStatisticsQuery+" AND users.id = ?", userid).Scan(&result).Error; err != nil {
		return result.UserStatistics, err
	}
	traffic, err := GetUserTrafficTotals([]uint{userid})
	result.RxSum, result.TxSum = traffic[userid].RX, traffic[userid].TX
	return result.UserStatistics, err
}

func GetUsersWithStatistics() ([]UserWithStatistics, error) {
	result := make([]UserWithStatistics, 0)
	if err := storage.Get().Raw(userStatisticsQuery + " ORDER BY users.id").Scan(&result).Error; err != nil {
		return nil, err
	}
	traffic, err := GetUserTrafficTotals(nil)
	if err != nil {
		return nil, err
	}
	for index := range result {
		result[index].RxSum, result[index].TxSum = traffic[result[index].ID].RX, traffic[result[index].ID].TX
	}
	return result, nil
}
