package candy

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lanthora/cacao/logger"
	"github.com/lanthora/cacao/model"
	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

func init() {
	idNetMap = map[uint]*Net{}

	for _, netModel := range model.GetNets() {
		InsertNet(&netModel)
	}
}

type Net struct {
	model        *model.Net
	ipWsMap      map[uint32]*candysocket
	ipWsMapMutex sync.RWMutex
	connections  map[*candysocket]struct{}
	closed       bool
	dhcpMutex    sync.Mutex

	net  uint32
	host uint32
	mask uint32
}

var idNetMap map[uint]*Net
var idNetMapMutex sync.RWMutex

// FlushDeviceState is driven by the shared background task scheduler. Idle
// online devices still refresh activity; individual packets never write SQL.
func FlushDeviceState(ctx context.Context) (int, error) {
	idNetMapMutex.RLock()
	for _, n := range idNetMap {
		n.ipWsMapMutex.RLock()
		for _, ws := range n.ipWsMap {
			ws.dev.mutex.Lock()
			if ws.dev.model.Online {
				model.QueueDeviceState(*ws.dev.model, ws.dev.generation)
			}
			ws.dev.mutex.Unlock()
		}
		n.ipWsMapMutex.RUnlock()
	}
	idNetMapMutex.RUnlock()
	return model.FlushDeviceStates(ctx)
}

func (n *Net) ipConflict(ip, vmac string) bool {
	db := storage.Get()
	device := &model.Device{NetID: n.model.ID, IP: ip}
	result := db.Where(device).Take(device)
	if result.Error == gorm.ErrRecordNotFound {
		return false
	}
	if result.Error == nil && device.VMac == vmac {
		return false
	}

	return true
}

func (n *Net) checkAuthMessage(message *AuthMessage) error {
	if err := checkMessageTimestamp(message.Timestamp); err != nil {
		return err
	}
	reported := message.Hash

	var data []byte
	data = append(data, n.model.Password...)
	data = binary.BigEndian.AppendUint32(data, message.IP)
	data = binary.BigEndian.AppendUint64(data, uint64(message.Timestamp))

	expected := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(expected[:], reported[:]) != 1 {
		return fmt.Errorf("auth check failed: hash does not match")
	}
	return nil
}

func (n *Net) checkDHCPMessage(message *DHCPMessage) error {
	if err := checkMessageTimestamp(message.Timestamp); err != nil {
		return err
	}
	reported := message.Hash

	var data []byte
	data = append(data, n.model.Password...)
	data = binary.BigEndian.AppendUint64(data, uint64(message.Timestamp))

	expected := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(expected[:], reported[:]) != 1 {
		return fmt.Errorf("dhcp check failed: hash does not match")
	}
	return nil
}

func (n *Net) checkVMacMessage(message *VMacMessage) error {
	if err := checkMessageTimestamp(message.Timestamp); err != nil {
		return err
	}
	if _, err := strconv.ParseUint(message.VMac, 16, 64); len(message.VMac) != 16 || err != nil {
		return fmt.Errorf("vmac check failed: invalid vmac")
	}

	reported := message.Hash

	var data []byte
	data = append(data, n.model.Password...)
	data = append(data, message.VMac...)
	data = binary.BigEndian.AppendUint64(data, uint64(message.Timestamp))

	expected := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(expected[:], reported[:]) != 1 {
		return fmt.Errorf("vmac check failed: hash does not match")
	}
	return nil
}

func (n *Net) updateHost() string {
	for ok := true; ok; ok = (n.host == 0 || n.host == ^n.mask) {
		n.host = (n.host + 1) & (^n.mask)
	}
	return uint32ToStrIp(n.net | n.host)
}

func checkMessageTimestamp(timestamp int64) error {
	// Candy clients use Unix seconds. Compare bounds instead of subtracting
	// untrusted int64 values, which could overflow.
	now := time.Now().Unix()
	if timestamp < now-300 || timestamp > now+300 {
		return fmt.Errorf("authentication timestamp outside the five-minute window")
	}
	return nil
}

func (n *Net) close() {
	n.ipWsMapMutex.Lock()
	defer n.ipWsMapMutex.Unlock()
	n.closed = true
	// Include connections that have not finished authentication. Otherwise a
	// connection could authenticate with the old password after a rotation.
	for ws := range n.connections {
		ws.authenticated.Store(false)
		ws.conn.Close()
	}
	for ip, ws := range n.ipWsMap {
		ws.dev.mutex.Lock()
		ws.dev.model.Online = false
		model.QueueDeviceState(*ws.dev.model, ws.dev.generation)
		ws.dev.mutex.Unlock()
		delete(n.ipWsMap, ip)
	}
}

func IsInvalidDHCP(cidr string) bool {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil || ipNet.IP.To4() == nil || len(ipNet.Mask) != net.IPv4len {
		return true
	}
	mask := binary.BigEndian.Uint32(ipNet.Mask)
	return ^mask < 2
}

func InsertNet(netModel *model.Net) {
	idNetMapMutex.Lock()
	defer idNetMapMutex.Unlock()
	insertNetLocked(netModel)
}

func insertNetLocked(netModel *model.Net) {
	if IsInvalidDHCP(netModel.DHCP) {
		logger.Fatal("invalid net cidr: %v", netModel.DHCP)
		return
	}

	_, ipNet, _ := net.ParseCIDR(netModel.DHCP)
	netid := binary.BigEndian.Uint32(ipNet.IP)
	mask := binary.BigEndian.Uint32(ipNet.Mask)
	hostid := rand.Uint32() & ^mask

	net := &Net{
		model:       netModel,
		ipWsMap:     make(map[uint32]*candysocket),
		connections: make(map[*candysocket]struct{}),
		net:         netid,
		host:        hostid,
		mask:        mask,
	}
	net.updateHost()
	idNetMap[netModel.ID] = net
}

func UpdateNet(netModel *model.Net) {
	idNetMapMutex.Lock()
	defer idNetMapMutex.Unlock()
	if n := idNetMap[netModel.ID]; n != nil {
		n.close()
	}
	insertNetLocked(netModel)
}

func DeleteNet(netid uint) {
	idNetMapMutex.Lock()
	defer idNetMapMutex.Unlock()

	if net, ok := idNetMap[netid]; ok {
		net.close()
	}

	delete(idNetMap, netid)
}

func ReloadNet(netid uint) error {
	return syncNet(netid, true)
}

func getNetById(netid uint) *Net {
	idNetMapMutex.RLock()
	defer idNetMapMutex.RUnlock()

	if net, ok := idNetMap[netid]; ok {
		return net
	}
	return nil
}

func getNetByPath(path string) *Net {
	username := "@"
	netname := "@"

	result := strings.Split(strings.Trim(path, "/"), "/")
	if len(result) > 2 {
		return nil
	}
	if result[0] != "" {
		if !IsValidUsername(result[0]) {
			return nil
		}
		username = result[0]
	}
	if len(result) > 1 {
		if !IsAlphaNumeric(result[1]) {
			return nil
		}
		netname = result[1]
	}
	netid := model.GetNetIdByUsernameAndNetname(username, netname)
	return getNetById(netid)
}

func absInt64(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

func uint32ToStrIp(ip uint32) string {
	var buffer []byte = make([]byte, 4)
	binary.BigEndian.PutUint32(buffer, ip)
	return net.IP(buffer).String()
}

func strIpToUint32(ip string) uint32 {
	segs := strings.Split(ip, ".")
	if len(segs) != 4 {
		logger.Fatal("convert ip to uint32 failed: %v", ip)
	}

	s := make([]int, 4)
	for i := 0; i < 4; i++ {
		n, err := strconv.Atoi(segs[i])
		if err != nil || n < 0 || n > 255 {
			logger.Fatal("convert ip to uint32 failed: %v", ip)
		}
		s[i] = n
	}

	rv := uint32(s[0]<<24 | s[1]<<16 | s[2]<<8 | s[3])
	return rv
}
