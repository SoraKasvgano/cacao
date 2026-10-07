package candy

import (
	"sync"

	"github.com/lanthora/cacao/model"
)

type Device struct {
	mutex      sync.Mutex
	model      *model.Device
	ip         uint32
	generation uint64
}
