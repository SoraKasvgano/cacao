package candy

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/lanthora/cacao/logger"
	"github.com/lanthora/cacao/model"
	"github.com/lanthora/cacao/storage"
	"github.com/lunixbochs/struc"
)

const (
	maxWebsocketMessageSize = 65536 // Type byte plus the largest IPv4 packet.
	websocketAuthTimeout    = 15 * time.Second
	websocketWriteTimeout   = 10 * time.Second
)

func WebsocketMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		apiRoute := c.FullPath() == "/api" || strings.HasPrefix(c.FullPath(), "/api/")
		// Candy paths contain at most a username and network name. Preserve
		// networks owned by the valid username "api", while HTTP API routes
		// always keep their own authentication middleware.
		apiPath := strings.HasPrefix(path, "/api/") && strings.Count(strings.Trim(path, "/"), "/") >= 2
		if !apiRoute && !apiPath && websocket.IsWebSocketUpgrade(c.Request) {
			handleWebsocket(c)
			c.Abort()
		} else {
			c.Next()
		}
	}
}

func handleWebsocket(c *gin.Context) {
	net := getNetByPath(c.Request.URL.Path)
	if net == nil {
		c.Status(http.StatusNotFound)
		return
	}
	upgrader := websocket.Upgrader{
		HandshakeTimeout: websocketAuthTimeout,
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Debug("websocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()
	conn.SetReadLimit(maxWebsocketMessageSize)
	ws := &candysocket{ctx: c, conn: conn, net: net, authDeadline: time.Now().Add(websocketAuthTimeout)}
	net.ipWsMapMutex.Lock()
	if net.closed {
		net.ipWsMapMutex.Unlock()
		return
	}
	net.connections[ws] = struct{}{}
	net.ipWsMapMutex.Unlock()
	defer ws.disconnect()
	conn.SetPingHandler(func(buffer string) error { return ws.handlePingMessage(buffer) })

	for {
		if err := ws.updateReadDeadline(); err != nil {
			break
		}
		messageType, buffer, err := conn.ReadMessage()
		if err != nil {
			logger.Debug("read websocket failed: %v", err)
			break
		}
		if messageType != websocket.BinaryMessage {
			err = fmt.Errorf("only binary messages are supported")
		} else {
			err = ws.handleMessage(buffer)
		}
		if err != nil {
			logger.Debug("handle client message failed: %v", err)
			break
		}
	}

}

type candysocket struct {
	ctx           *gin.Context
	conn          *websocket.Conn
	connMutex     sync.Mutex
	dev           *Device
	net           *Net
	authenticated atomic.Bool
	authDeadline  time.Time
}

func (ws *candysocket) disconnect() {
	ws.net.ipWsMapMutex.Lock()
	defer ws.net.ipWsMapMutex.Unlock()
	ws.authenticated.Store(false)
	delete(ws.net.connections, ws)
	if ws.dev != nil && ws.net.ipWsMap[ws.dev.ip] == ws {
		delete(ws.net.ipWsMap, ws.dev.ip)
		ws.dev.mutex.Lock()
		defer ws.dev.mutex.Unlock()
		ws.dev.model.Online = false
		ws.dev.model.SaveRxTxOnline()
	}
}

func (ws *candysocket) handleMessage(buffer []byte) error {
	if len(buffer) == 0 || len(buffer) > maxWebsocketMessageSize {
		return fmt.Errorf("invalid websocket message size")
	}
	switch buffer[0] {
	case AUTH:
		return ws.handleAuthMessage(buffer)
	case FORWARD:
		return ws.handleForwardMessage(buffer)
	case DHCP:
		return ws.handleDHCPMessage(buffer)
	case PEER:
		return ws.handlePeerConnMessage(buffer)
	case VMAC:
		return ws.handleVMacMessage(buffer)
	case DISCOVERY:
		return ws.handleDiscoveryMessage(buffer)
	case GENERAL:
		return ws.handleGeneralMessage(buffer)
	default:
		return fmt.Errorf("unsupported websocket message type")
	}
}

func (ws *candysocket) updateReadDeadline() error {
	deadline := time.Now().Add(60 * time.Second)
	if !ws.authenticated.Load() {
		deadline = ws.authDeadline
	}
	return ws.conn.SetReadDeadline(deadline)
}

func (ws *candysocket) writeCloseMessage(text string) error {
	ws.connMutex.Lock()
	defer ws.connMutex.Unlock()
	return ws.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, text), time.Now().Add(websocketWriteTimeout))
}

func (ws *candysocket) writeMessage(buffer []byte) error {
	ws.connMutex.Lock()
	defer ws.connMutex.Unlock()
	if err := ws.conn.SetWriteDeadline(time.Now().Add(websocketWriteTimeout)); err != nil {
		return err
	}
	return ws.conn.WriteMessage(websocket.BinaryMessage, buffer)
}

func (ws *candysocket) writePong(buffer []byte) error {
	ws.connMutex.Lock()
	defer ws.connMutex.Unlock()
	return ws.conn.WriteControl(websocket.PongMessage, buffer, time.Now().Add(websocketWriteTimeout))
}

func (ws *candysocket) handlePingMessage(buffer string) error {
	if err := ws.updateReadDeadline(); err != nil {
		return err
	}

	if !ws.authenticated.Load() {
		logger.Debug("ping failed: the client is not logged in: %v", buffer)
		return nil
	}

	info := strings.Split(buffer, "::")
	if len(info) < 3 || info[0] != "candy" {
		logger.Debug("ping failed: invalid format: %v", buffer)
		return nil
	}

	ws.dev.mutex.Lock()
	defer ws.dev.mutex.Unlock()
	ws.dev.model.OS = info[1]
	ws.dev.model.Version = info[2]

	if len(info) > 3 {
		ws.dev.model.Hostname = info[3]
	}

	if ws.dev.model.Online {
		ws.dev.model.SaveOsVersionHostname()
	}

	return ws.writePong([]byte(buffer))
}

func (ws *candysocket) handleAuthMessage(buffer []byte) error {
	if ws.authenticated.Load() || ws.dev == nil {
		return fmt.Errorf("auth failed: unexpected authentication message")
	}
	r := bytes.NewReader(buffer)
	message := &AuthMessage{}
	if err := struc.Unpack(r, message); err != nil {
		return err
	}

	if err := ws.net.checkAuthMessage(message); err != nil {
		return err
	}

	if ws.net.net != ws.net.mask&message.IP || (^ws.net.mask)&(message.IP) == 0 || (^ws.net.mask)&(message.IP+1) == 0 {
		ws.writeCloseMessage("ip invalid")
		return fmt.Errorf("auth failed: network does not match")
	}

	ws.net.ipWsMapMutex.Lock()
	defer ws.net.ipWsMapMutex.Unlock()
	if ws.net.closed {
		return fmt.Errorf("auth failed: network has been revoked")
	}
	if ws.net.ipConflict(uint32ToStrIp(message.IP), ws.dev.model.VMac) {
		ws.writeCloseMessage("ip conflict")
		return fmt.Errorf("auth failed: ip conflict: %v", uint32ToStrIp(message.IP))
	}

	if oldws, ok := ws.net.ipWsMap[message.IP]; ok {
		oldws.authenticated.Store(false)
		oldws.dev.mutex.Lock()
		oldws.dev.model.Online = false
		oldws.dev.model.SaveRxTxOnline()
		oldws.dev.mutex.Unlock()
		oldws.conn.Close()
	}

	ws.dev.ip = message.IP
	ws.net.ipWsMap[message.IP] = ws

	db := storage.Get()
	db.Where(ws.dev.model).First(ws.dev.model)
	ws.dev.model.IP = uint32ToStrIp(message.IP)
	ws.dev.model.Online = true
	ws.dev.model.Country, ws.dev.model.Region = GetLocation(net.ParseIP(ws.ctx.ClientIP()))
	ws.dev.model.Save()
	ws.authenticated.Store(true)

	ws.updateSystemRoute()
	return nil
}

func (ws *candysocket) handleForwardMessage(buffer []byte) error {
	if !ws.authenticated.Load() {
		return fmt.Errorf("forward failed: conn is not logged in")
	}

	r := bytes.NewReader(buffer)
	message := &ForwardMessage{}
	if err := struc.Unpack(r, message); err != nil {
		return err
	}

	ws.addTX(len(buffer))

	ws.net.ipWsMapMutex.RLock()
	defer ws.net.ipWsMapMutex.RUnlock()
	if !ws.authenticated.Load() {
		return fmt.Errorf("forward failed: authentication has been revoked")
	}

	if dstWs, ok := ws.net.ipWsMap[message.Dst]; ok {
		dstWs.writeMessage(buffer)
		dstWs.addRX(len(buffer))
	}

	broadcast := func() bool {
		if !ws.net.model.Broadcast {
			return false
		}
		if ws.net.net|^ws.net.mask == message.Dst {
			return true
		}
		if message.Dst&0xF0000000 == 0xE0000000 {
			return true
		}
		if message.Dst == 0xFFFFFFFF {
			return true
		}
		return false
	}()

	if broadcast {
		for _, dstWs := range ws.net.ipWsMap {
			if dstWs != ws && dstWs.authenticated.Load() {
				dstWs.writeMessage(buffer)
				dstWs.addRX(len(buffer))
			}
		}
	}

	return nil
}

func (ws *candysocket) handleDHCPMessage(buffer []byte) error {
	if ws.dev == nil || ws.dev.model == nil || ws.authenticated.Load() {
		return fmt.Errorf("dhcp failed: unexpected DHCP message")
	}
	r := bytes.NewReader(buffer)
	message := &DHCPMessage{}
	if err := struc.Unpack(r, message); err != nil {
		return err
	}

	if err := ws.net.checkDHCPMessage(message); err != nil {
		return err
	}

	db := storage.Get()
	ws.net.dhcpMutex.Lock()
	defer ws.net.dhcpMutex.Unlock()

	// 检查能否使用数据库中地址, 地址可用时更新响应结果
	canUseLatestAddress := func() bool {
		device := model.Device{}
		if db.Where(&model.Device{NetID: ws.net.model.ID, VMac: ws.dev.model.VMac}).Take(&device).Error != nil {
			return false
		}
		ip := net.ParseIP(device.IP)
		if ip == nil {
			return false
		}
		ip = ip.To4()
		if ip == nil {
			return false
		}

		if binary.BigEndian.Uint32(ip)&ws.net.mask != ws.net.net {
			return false
		}
		if ws.net.ipConflict(device.IP, ws.dev.model.VMac) {
			return false
		}

		ipNet := net.IPNet{IP: ip, Mask: make(net.IPMask, 4)}
		binary.BigEndian.PutUint32(ipNet.Mask, ws.net.mask)
		message.Cidr = []byte(ipNet.String())
		return true
	}()

	// 根据用户传入的地址检查是否需要生成新地址
	needGenNewAddress := func() bool {
		if canUseLatestAddress {
			return false
		}
		cidr := func(input []byte) string {
			if end := bytes.IndexByte(input, 0); end >= 0 {
				input = input[:end]
			}
			return string(input)
		}(message.Cidr)

		ip, ipNet, err := net.ParseCIDR(cidr)
		if err != nil || ip.To4() == nil || len(ipNet.Mask) != net.IPv4len {
			return true
		}
		if binary.BigEndian.Uint32(ipNet.IP.To4()) != ws.net.net {
			return true
		}
		if binary.BigEndian.Uint32(ipNet.Mask) != ws.net.mask {
			return true
		}
		host := binary.BigEndian.Uint32(ip.To4()) & ^ws.net.mask
		if host == 0 || host == ^ws.net.mask {
			return true
		}
		devices := []model.Device{}
		db.Where(&model.Device{NetID: ws.net.model.ID, IP: ip.String()}).Find(&devices)
		if len(devices) > 1 {
			return true
		}
		if len(devices) == 0 {
			return false
		}
		if len(devices) == 1 && devices[0].VMac == ws.dev.model.VMac {
			return false
		}
		return true
	}()

	// 用户传入的地址也不可用时, 生成一个新地址, 并更新响应结果
	// One query bounds the database work even for a large or exhausted subnet.
	var assignedIPs []string
	if needGenNewAddress {
		if err := db.Model(&model.Device{}).Where("net_id = ?", ws.net.model.ID).Pluck("ip", &assignedIPs).Error; err != nil {
			return err
		}
	}
	assigned := make(map[string]struct{}, len(assignedIPs))
	for _, ip := range assignedIPs {
		assigned[ip] = struct{}{}
	}
	maxAttempts := uint64(len(assigned)) + 1
	if capacity := uint64(^ws.net.mask) - 1; maxAttempts > capacity {
		maxAttempts = capacity
	}
	for attempts := uint64(0); needGenNewAddress; attempts++ {
		if attempts >= maxAttempts {
			ws.writeCloseMessage("not enough address")
			return fmt.Errorf("dhcp failed: not enough address")
		}
		if _, used := assigned[ws.net.updateHost()]; !used {
			ipNet := net.IPNet{IP: make(net.IP, 4), Mask: make(net.IPMask, 4)}
			binary.BigEndian.PutUint32(ipNet.IP, ws.net.net|ws.net.host)
			binary.BigEndian.PutUint32(ipNet.Mask, ws.net.mask)
			message.Cidr = []byte(ipNet.String())
			break
		}
	}

	var output bytes.Buffer
	if err := struc.Pack(&output, message); err != nil {
		return err
	}
	return ws.writeMessage(output.Bytes())
}

func (ws *candysocket) handlePeerConnMessage(buffer []byte) error {
	if !ws.authenticated.Load() {
		return fmt.Errorf("peer conn failed: conn is not logged in")
	}

	r := bytes.NewReader(buffer)
	message := &PeerConnMessage{}
	if err := struc.Unpack(r, message); err != nil {
		return err
	}

	if ws.dev.ip != message.Src {
		return fmt.Errorf("peer conn failed: source address does not match login information")
	}

	ws.net.ipWsMapMutex.RLock()
	defer ws.net.ipWsMapMutex.RUnlock()
	if !ws.authenticated.Load() {
		return fmt.Errorf("peer conn failed: authentication has been revoked")
	}

	if dstWs, ok := ws.net.ipWsMap[message.Dst]; ok {
		dstWs.writeMessage(buffer)
	}

	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, message.IP)
	country, region := GetLocation(ip)
	ws.dev.mutex.Lock()
	defer ws.dev.mutex.Unlock()
	ws.dev.model.Country, ws.dev.model.Region = country, region
	ws.dev.model.Save()

	return nil
}

func (ws *candysocket) handleVMacMessage(buffer []byte) error {
	if ws.dev != nil || ws.authenticated.Load() {
		return fmt.Errorf("vmac failed: identity is already set")
	}
	r := bytes.NewReader(buffer)
	message := &VMacMessage{}
	if err := struc.Unpack(r, message); err != nil {
		return err
	}

	if err := ws.net.checkVMacMessage(message); err != nil {
		return err
	}
	ws.dev = &Device{model: &model.Device{NetID: ws.net.model.ID, VMac: message.VMac}}
	return nil
}

func (ws *candysocket) handleDiscoveryMessage(buffer []byte) error {
	if !ws.authenticated.Load() {
		return fmt.Errorf("discovery failed: conn is not logged in")
	}

	r := bytes.NewReader(buffer)
	message := &DiscoveryMessage{}
	if err := struc.Unpack(r, message); err != nil {
		return err
	}

	if ws.dev.ip != message.Src {
		return fmt.Errorf("discovery failed: source address does not match login information")
	}

	ws.addTX(len(buffer))

	ws.net.ipWsMapMutex.RLock()
	defer ws.net.ipWsMapMutex.RUnlock()
	if !ws.authenticated.Load() {
		return fmt.Errorf("discovery failed: authentication has been revoked")
	}

	if dstWs, ok := ws.net.ipWsMap[message.Dst]; ok {
		dstWs.writeMessage(buffer)
		dstWs.addRX(len(buffer))
	}

	if uint32(0xFFFFFFFF) == message.Dst {
		for _, dstWs := range ws.net.ipWsMap {
			if dstWs != ws && dstWs.authenticated.Load() {
				dstWs.writeMessage(buffer)
				dstWs.addRX(len(buffer))
			}
		}
	}

	return nil
}

func (ws *candysocket) handleGeneralMessage(buffer []byte) error {
	if !ws.authenticated.Load() {
		return fmt.Errorf("general failed: conn is not logged in")
	}

	r := bytes.NewReader(buffer)
	message := &GeneralMessage{}
	if err := struc.Unpack(r, message); err != nil {
		return err
	}

	if ws.dev.ip != message.Src {
		return fmt.Errorf("general failed: source address does not match login information")
	}

	ws.addTX(len(buffer))

	ws.net.ipWsMapMutex.RLock()
	defer ws.net.ipWsMapMutex.RUnlock()
	if !ws.authenticated.Load() {
		return fmt.Errorf("general failed: authentication has been revoked")
	}

	if dstWs, ok := ws.net.ipWsMap[message.Dst]; ok {
		dstWs.writeMessage(buffer)
		dstWs.addRX(len(buffer))
	}

	if ws.net.model.Broadcast && uint32(0xFFFFFFFF) == message.Dst {
		for _, dstWs := range ws.net.ipWsMap {
			if dstWs != ws && dstWs.authenticated.Load() {
				dstWs.writeMessage(buffer)
				dstWs.addRX(len(buffer))
			}
		}
	}

	return nil
}

func (ws *candysocket) updateSystemRoute() {
	header := &RouteMessage{Type: ROUTE, Size: 0, Reserved: 0}
	bodyBuffer := bytes.Buffer{}

	db := storage.Get()
	routes := []model.Route{}
	db.Where(&model.Route{NetID: ws.net.model.ID}).Order("priority").Find(&routes)
	for _, route := range routes {
		if header.Size == 255 {
			break
		}
		deviceAddr := strIpToUint32(route.DevAddr)
		deviceMask := strIpToUint32(route.DevMask)
		if deviceAddr != deviceMask&ws.dev.ip {
			continue
		}
		header.Size += 1
		destAddr := strIpToUint32(route.DstAddr)
		destMask := strIpToUint32(route.DstMask)
		nextHop := strIpToUint32(route.NextHop)
		body := &RouteMessageEntry{Dest: destAddr, Mask: destMask, NextHop: nextHop}
		struc.Pack(&bodyBuffer, body)
	}
	if header.Size > 0 {
		headerBuffer := bytes.Buffer{}
		struc.Pack(&headerBuffer, header)
		ws.writeMessage(append(headerBuffer.Bytes(), bodyBuffer.Bytes()...))
	}
}

func (ws *candysocket) addTX(size int) {
	ws.dev.mutex.Lock()
	defer ws.dev.mutex.Unlock()
	ws.dev.model.TX += uint64(size)
}

func (ws *candysocket) addRX(size int) {
	ws.dev.mutex.Lock()
	defer ws.dev.mutex.Unlock()
	ws.dev.model.RX += uint64(size)
}
