package candy

import (
	"sync"
	"time"

	"github.com/lanthora/cacao/model"
)

type Device struct {
	mutex      sync.Mutex
	model      *model.Device
	ip         uint32
	generation uint64
}

func init() {
	go autoCleanInactiveDevice()
}

func autoCleanInactiveDevice() {
	nets := model.GetNets()
	for _, n := range nets {
		if n.Lease > 0 {
			devices := model.GetDevicesByNetID(n.ID)
			for _, d := range devices {
				if d.UpdatedAt.AddDate(0, 0, int(n.Lease)).Before(time.Now()) {
					d.Delete()
				}
			}
		}
	}
	time.AfterFunc(time.Hour, autoCleanInactiveDevice)
}
