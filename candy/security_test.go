package candy

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/lanthora/cacao/model"
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
