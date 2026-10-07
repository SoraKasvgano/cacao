package candy

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/lanthora/cacao/model"
	"github.com/lanthora/cacao/storage"
	"github.com/lunixbochs/struc"
)

func signedMessage(t *testing.T, password string, message interface{}) []byte {
	t.Helper()
	data := []byte(password)
	switch m := message.(type) {
	case *VMacMessage:
		data = append(data, m.VMac...)
		data = binary.BigEndian.AppendUint64(data, uint64(m.Timestamp))
		m.Hash = sha256.Sum256(data)
	case *AuthMessage:
		data = binary.BigEndian.AppendUint32(data, m.IP)
		data = binary.BigEndian.AppendUint64(data, uint64(m.Timestamp))
		m.Hash = sha256.Sum256(data)
	case *DHCPMessage:
		data = binary.BigEndian.AppendUint64(data, uint64(m.Timestamp))
		m.Hash = sha256.Sum256(data)
	}
	var output bytes.Buffer
	if err := struc.Pack(&output, message); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func newTestWebsocketNetwork(t *testing.T, cidr string) (*Net, string) {
	t.Helper()
	return newNamedTestWebsocketNetwork(t, cidr, fmt.Sprintf("ws%x", time.Now().UnixNano()), "secure")
}

func newNamedTestWebsocketNetwork(t *testing.T, cidr, username, netname string) (*Net, string) {
	t.Helper()
	user := &model.User{Name: username, Role: "normal"}
	db := storage.Get()
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	network := &model.Net{UserID: user.ID, Name: netname, Password: "test-password", DHCP: cidr}
	if err := db.Create(network).Error; err != nil {
		t.Fatal(err)
	}
	InsertNet(network)
	n := getNetById(network.ID)
	t.Cleanup(func() {
		DeleteNet(network.ID)
		db.Unscoped().Where("net_id = ?", network.ID).Delete(&model.Device{})
		db.Unscoped().Delete(network)
		db.Unscoped().Delete(user)
	})
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.SetTrustedProxies(nil)
	router.Use(WebsocketMiddleware())
	router.GET("/api/protected", func(c *gin.Context) { c.Status(http.StatusUnauthorized) })
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/" + user.Name
	if network.Name != "@" {
		url += "/" + network.Name
	}
	return n, url
}

func dialTestWebsocket(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	conn, _, err := dialTestWebsocketRequest(t, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	t.Cleanup(func() { conn.Close() })
	return conn
}

func dialTestWebsocketRequest(t *testing.T, url string, headers http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	for attempt := 0; ; attempt++ {
		conn, response, err := websocket.DefaultDialer.Dial(url, headers)
		var dialError *net.OpError
		// Windows exposes WSAEADDRINUSE (10048), separately from Go's
		// synthetic syscall.EADDRINUSE. Retry only this local dial failure;
		// server responses and other network/protocol failures remain visible.
		addressInUse := errors.Is(err, syscall.EADDRINUSE) ||
			(runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(10048)))
		if attempt >= 2 || !addressInUse || !errors.As(err, &dialError) || dialError.Op != "dial" {
			return conn, response, err
		}
		t.Logf("retrying transient local dial address collision: %v", err)
		time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
	}
}

func sendVMac(t *testing.T, conn *websocket.Conn, n *Net, vmac string) {
	t.Helper()
	message := &VMacMessage{Type: VMAC, VMac: vmac, Timestamp: time.Now().Unix()}
	if err := conn.WriteMessage(websocket.BinaryMessage, signedMessage(t, n.model.Password, message)); err != nil {
		t.Fatal(err)
	}
}

func authenticateTestWebsocket(t *testing.T, conn *websocket.Conn, n *Net, vmac string, ip uint32) {
	t.Helper()
	sendVMac(t, conn, n, vmac)
	message := &AuthMessage{Type: AUTH, IP: ip, Timestamp: time.Now().Unix()}
	if err := conn.WriteMessage(websocket.BinaryMessage, signedMessage(t, n.model.Password, message)); err != nil {
		t.Fatal(err)
	}
	// An echoed discovery message confirms AUTH completed without changing the
	// existing protocol, which does not send a separate authentication reply.
	echo := signedMessage(t, "", &DiscoveryMessage{Type: DISCOVERY, Src: ip, Dst: ip})
	if err := conn.WriteMessage(websocket.BinaryMessage, echo); err != nil {
		t.Fatal(err)
	}
	if _, response, err := conn.ReadMessage(); err != nil || !bytes.Equal(response, echo) {
		t.Fatalf("authentication did not complete: %x, %v", response, err)
	}
}

func TestAuthenticationTimestamps(t *testing.T) {
	n := &Net{model: &model.Net{Password: "secret"}}
	for _, timestamp := range []int64{time.Now().Unix(), time.Now().Unix() - 299, time.Now().Unix() + 299, 0, math.MinInt64, math.MaxInt64, time.Now().Unix() - 301, time.Now().Unix() + 301} {
		t.Run(fmt.Sprint(timestamp), func(t *testing.T) {
			wantValid := timestamp >= time.Now().Unix()-300 && timestamp <= time.Now().Unix()+300
			auth := &AuthMessage{Type: AUTH, IP: 0x0a000001, Timestamp: timestamp}
			dhcp := &DHCPMessage{Type: DHCP, Cidr: make([]byte, 32), Timestamp: timestamp}
			vmac := &VMacMessage{Type: VMAC, VMac: "0123456789abcdef", Timestamp: timestamp}
			signedMessage(t, n.model.Password, auth)
			signedMessage(t, n.model.Password, dhcp)
			signedMessage(t, n.model.Password, vmac)
			for name, err := range map[string]error{"auth": n.checkAuthMessage(auth), "dhcp": n.checkDHCPMessage(dhcp), "vmac": n.checkVMacMessage(vmac)} {
				if (err == nil) != wantValid {
					t.Errorf("%s accepted=%v, want %v", name, err == nil, wantValid)
				}
			}
			auth.Hash[0] ^= 1
			dhcp.Hash[0] ^= 1
			vmac.Hash[0] ^= 1
			if n.checkAuthMessage(auth) == nil || n.checkDHCPMessage(dhcp) == nil || n.checkVMacMessage(vmac) == nil {
				t.Fatal("invalid signature accepted")
			}
		})
	}
}

func TestWebsocketHandshakeAndFrameLimits(t *testing.T) {
	_, url := newTestWebsocketNetwork(t, "10.20.0.0/24")
	for _, origin := range []string{"https://attacker.invalid", "null"} {
		conn, response, err := dialTestWebsocketRequest(t, url, http.Header{"Origin": []string{origin}})
		if conn != nil {
			conn.Close()
		}
		if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
			t.Fatalf("cross-origin upgrade accepted: %v, %v", response, err)
		}
	}
	for _, payload := range [][]byte{{}, make([]byte, maxWebsocketMessageSize+1)} {
		conn := dialTestWebsocket(t, url)
		conn.WriteMessage(websocket.BinaryMessage, payload)
		if _, _, err := conn.ReadMessage(); err == nil {
			t.Fatal("malformed websocket frame was not rejected")
		}
	}
	baseURL := url[:strings.Index(url[5:], "/")+5]
	_, response, err := dialTestWebsocketRequest(t, baseURL+"/api/protected", nil)
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("websocket bypassed API routing: %v, %v", response, err)
	}
}
