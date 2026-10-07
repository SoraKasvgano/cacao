package candy

import (
	"context"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

func TestShutdownWaitsForSocketsAndFlushesDisconnects(t *testing.T) {
	n, url := newTestWebsocketNetwork(t, "10.20.0.0/24")
	online := dialTestWebsocket(t, url)
	authenticateTestWebsocket(t, online, n, "1000000000000012", 0x0a140001)
	pending := dialTestWebsocket(t, url)
	sendVMac(t, pending, n, "1000000000000013")
	t.Cleanup(func() {
		// Production shutdown is terminal. Restore admission only for tests.
		websocketLifecycle.Lock()
		websocketLifecycle.stopping = false
		websocketLifecycle.Unlock()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ShutdownConnections(ctx); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*websocket.Conn{online, pending} {
		if _, _, err := conn.ReadMessage(); err == nil {
			t.Fatal("websocket survived shutdown")
		}
	}
	n.ipWsMapMutex.RLock()
	remaining := len(n.connections)
	n.ipWsMapMutex.RUnlock()
	if remaining != 0 {
		t.Fatalf("shutdown left %d connection callbacks running", remaining)
	}
	var persisted model.Device
	if err := storage.Get().Where("net_id = ? AND vmac = ?", n.model.ID, "1000000000000012").First(&persisted).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Online || persisted.RX == 0 {
		t.Fatalf("shutdown did not persist final telemetry: %+v", persisted)
	}
}
