package candy

import (
	"context"
	"sync"
)

var websocketLifecycle = struct {
	sync.Mutex
	stopping    bool
	connections map[*candysocket]struct{}
}{connections: make(map[*candysocket]struct{})}
var websocketHandlers sync.WaitGroup

func beginWebsocketHandler() bool {
	websocketLifecycle.Lock()
	defer websocketLifecycle.Unlock()
	if websocketLifecycle.stopping {
		return false
	}
	websocketHandlers.Add(1)
	return true
}

func endWebsocketHandler() { websocketHandlers.Done() }

func trackWebsocket(ws *candysocket) bool {
	websocketLifecycle.Lock()
	defer websocketLifecycle.Unlock()
	if websocketLifecycle.stopping {
		return false
	}
	websocketLifecycle.connections[ws] = struct{}{}
	return true
}

func untrackWebsocket(ws *candysocket) {
	websocketLifecycle.Lock()
	delete(websocketLifecycle.connections, ws)
	websocketLifecycle.Unlock()
}

// ShutdownConnections stops websocket admission, closes even connections from
// retired networks, waits for disconnect callbacks, then persists final state.
// Stop the HTTP server and background producers before calling this function.
func ShutdownConnections(ctx context.Context) error {
	websocketLifecycle.Lock()
	websocketLifecycle.stopping = true
	connections := make([]*candysocket, 0, len(websocketLifecycle.connections))
	for ws := range websocketLifecycle.connections {
		connections = append(connections, ws)
	}
	websocketLifecycle.Unlock()
	for _, ws := range connections {
		ws.authenticated.Store(false)
		_ = ws.conn.Close()
	}
	done := make(chan struct{})
	go func() { websocketHandlers.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		_, err := FlushDeviceState(ctx)
		return err
	}
}
