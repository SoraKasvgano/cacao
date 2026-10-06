package api

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/lanthora/cacao/candy"
	"github.com/lanthora/cacao/model"
	"gorm.io/gorm"
)

func NetShow(c *gin.Context) {
	user := c.MustGet("user").(*model.User)
	nets := model.GetNetsByUserID(user.ID)

	type netinfo struct {
		NetID     uint   `json:"netid"`
		Netname   string `json:"netname"`
		Password  string `json:"password"`
		DHCP      string `json:"dhcp"`
		Broadcast bool   `json:"broadcast"`
		Lease     uint   `json:"lease"`
	}

	response := make([]netinfo, 0)
	for _, n := range nets {
		response = append(response, netinfo{
			NetID:     n.ID,
			Netname:   n.Name,
			Password:  n.Password,
			DHCP:      n.DHCP,
			Broadcast: n.Broadcast,
			Lease:     n.Lease,
		})
	}

	setResponseData(c, gin.H{
		"nets": response,
	})
}

func NetInsert(c *gin.Context) {
	var request struct {
		Netname   string `json:"netname"`
		Password  string `json:"password"`
		DHCP      string `json:"dhcp"`
		Broadcast bool   `json:"broadcast"`
		Lease     uint   `json:"lease"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}

	if isInvalidNetname(request.Netname) {
		setErrorCode(c, InvalidNetworkName)
		return
	}

	if candy.IsInvalidDHCP(request.DHCP) {
		setErrorCode(c, InvalidDhcp)
		return
	}

	user := c.MustGet("user").(*model.User)

	if user.Name == "@" && request.Netname != "@" {
		setErrorCode(c, InvalidNetworkName)
		return
	}

	netModel := &model.Net{
		UserID: user.ID,
		Name:   request.Netname,
	}

	netModel.Password = request.Password
	netModel.DHCP = request.DHCP
	netModel.Broadcast = request.Broadcast
	netModel.Lease = request.Lease
	if err := netModel.Create(); err != nil {
		if errors.Is(err, model.ErrConflict) {
			setErrorCode(c, NetworkAlreadyExists)
		} else {
			setErrorCode(c, Unexpected)
		}
		return
	}
	if !writeSucceeded(c, candy.SyncNet(netModel.ID)) {
		return
	}

	setResponseData(c, gin.H{
		"netid":     netModel.ID,
		"netname":   netModel.Name,
		"password":  netModel.Password,
		"dhcp":      netModel.DHCP,
		"broadcast": netModel.Broadcast,
		"lease":     netModel.Lease,
	})
}

func NetEdit(c *gin.Context) {
	var request struct {
		NetID     uint   `json:"netid"`
		Netname   string `json:"netname"`
		Password  string `json:"password"`
		DHCP      string `json:"dhcp"`
		Broadcast bool   `json:"broadcast"`
		Lease     uint   `json:"lease"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}

	if isInvalidNetname(request.Netname) {
		setErrorCode(c, InvalidNetworkName)
		return
	}

	if candy.IsInvalidDHCP(request.DHCP) {
		setErrorCode(c, InvalidDhcp)
		return
	}

	user := c.MustGet("user").(*model.User)

	if user.Name == "@" && request.Netname != "@" {
		setErrorCode(c, InvalidNetworkName)
		return
	}

	netModel := model.GetNetByNetID(request.NetID)
	if netModel.UserID != user.ID {
		setErrorCode(c, NetworkNotExists)
		return
	}

	netModel.Name = request.Netname
	netModel.Password = request.Password
	netModel.DHCP = request.DHCP
	netModel.Broadcast = request.Broadcast
	netModel.Lease = request.Lease
	if err := netModel.Update(); err != nil {
		if errors.Is(err, model.ErrConflict) {
			setErrorCode(c, NetworkAlreadyExists)
		} else {
			setErrorCode(c, Unexpected)
		}
		return
	}
	if !writeSucceeded(c, candy.SyncNet(netModel.ID)) {
		return
	}

	setResponseData(c, gin.H{
		"netid":     netModel.ID,
		"netname":   netModel.Name,
		"password":  netModel.Password,
		"dhcp":      netModel.DHCP,
		"broadcast": netModel.Broadcast,
		"lease":     netModel.Lease,
	})
}

func NetDelete(c *gin.Context) {
	var request struct {
		ID uint `json:"netid"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}

	user := c.MustGet("user").(*model.User)
	if err := model.DeleteNetworkTree(request.ID, user.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			setErrorCode(c, NetworkNotExists)
		} else {
			setErrorCode(c, Unexpected)
		}
		return
	}
	candy.DeleteNet(request.ID)

	setResponseData(c, gin.H{
		"id": request.ID,
	})
}

func isInvalidNetname(netname string) bool {
	if netname == "@" {
		return false
	}
	if len(netname) < 3 || len(netname) > 32 || !candy.IsAlphaNumeric(netname) {
		return true
	}
	return false
}
