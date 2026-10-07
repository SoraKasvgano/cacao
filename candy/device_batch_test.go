package candy

import (
	"context"
	"testing"

	"github.com/lanthora/cacao/model"
	"github.com/lanthora/cacao/storage"
)

func TestAuthenticatedPingBatchesMetadata(t *testing.T) {
	n, url := newTestWebsocketNetwork(t, "10.20.0.0/24")
	conn := dialTestWebsocket(t, url)
	authenticateTestWebsocket(t, conn, n, "1000000000000001", 0x0a140001)
	n.ipWsMapMutex.RLock()
	ws := n.ipWsMap[0x0a140001]
	n.ipWsMapMutex.RUnlock()
	if err := ws.handlePingMessage("candy::linux::v2::batch-host"); err != nil {
		t.Fatal(err)
	}
	var persisted model.Device
	if err := storage.Get().First(&persisted, ws.dev.model.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Hostname != "" {
		t.Fatal("ping wrote metadata before the shared flush")
	}
	if current := model.GetDeviceByDevID(persisted.ID); current.Hostname != "batch-host" || current.Version != "v2" {
		t.Fatalf("API did not see queued ping metadata: %+v", current)
	}
	if _, err := FlushDeviceState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := storage.Get().First(&persisted, persisted.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Hostname != "batch-host" || persisted.OS != "linux" {
		t.Fatal("shared flush did not persist metadata")
	}
}
