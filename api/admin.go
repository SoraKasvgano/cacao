package api

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lanthora/cacao/candy"
	"github.com/lanthora/cacao/model"
	"github.com/lanthora/cacao/storage"
	"gorm.io/gorm"
)

func AdminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		value, ok := c.Get("user")
		user, valid := value.(*model.User)
		if !ok || !valid || user == nil {
			setErrorCode(c, NotLoggedIn)
			c.Abort()
			return
		}
		path := c.FullPath()
		allowed := false
		if strings.HasPrefix(path, "/api/admin/") {
			allowed = user.Role == "admin"
		} else if path == "/api/user/info" || path == "/api/user/logout" || path == "/api/user/changePassword" {
			allowed = user.Role == "admin" || user.Role == "normal"
		} else {
			allowed = user.Role == "normal"
		}
		if !allowed {
			setErrorCode(c, PermissionDenied)
			c.Abort()
			return
		}
		c.Next()
	}
}

func AdminShowUsers(c *gin.Context) {
	users, err := model.GetUsersWithStatistics()
	if !writeSucceeded(c, err) {
		return
	}

	type userinfo struct {
		UserID         uint   `json:"userid"`
		Username       string `json:"username"`
		Role           string `json:"role"`
		RegTime        string `json:"regtime"`
		LastActiveTime string `json:"lastActiveTime"`
		NetNum         uint   `json:"netnum"`
		DevNum         uint   `json:"devnum"`
		RxSum          uint64 `json:"rxsum"`
		TxSum          uint64 `json:"txsum"`
	}

	response := make([]userinfo, 0)
	for _, u := range users {
		response = append(response, userinfo{
			UserID:         u.ID,
			Username:       u.Name,
			Role:           u.Role,
			RegTime:        u.CreatedAt.Format(time.DateTime),
			LastActiveTime: u.UpdatedAt.Format(time.DateTime),
			NetNum:         u.NetNum,
			DevNum:         u.DevNum,
			RxSum:          u.RxSum,
			TxSum:          u.TxSum,
		})
	}

	setResponseData(c, gin.H{
		"users": response,
	})
}

func AdminAddUser(c *gin.Context) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}
	if !candy.IsValidUsername(request.Username) {
		setErrorCode(c, InvalidUsername)
		return
	}
	if len(request.Password) == 0 {
		setErrorCode(c, InvalidPassword)
		return
	}

	user := model.User{
		Name:     request.Username,
		Password: hashUserPassword(request.Username, request.Password),
		Role:     "normal",
	}

	netModel := &model.Net{
		Name:      "@",
		Password:  randomString(8),
		DHCP:      "192.168.202.0/24",
		Broadcast: true,
	}
	if !writeSucceeded(c, createAccount(c, &user, netModel, false, false)) {
		return
	}
	if !writeSucceeded(c, candy.SyncNet(netModel.ID)) {
		return
	}
	setResponseData(c, gin.H{"name": user.Name, "role": user.Role})
}

func AdminDeleteUser(c *gin.Context) {
	var request struct {
		UserID uint `json:"userid"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}

	user := c.MustGet("user").(*model.User)
	if request.UserID == user.ID {
		setErrorCode(c, CannotDeleteAdmin)
		return
	}

	netIDs, err := model.DeleteUserTree(request.UserID)
	if !writeSucceeded(c, err) {
		return
	}
	for _, id := range netIDs {
		candy.DeleteNet(id)
	}
	setResponseData(c, nil)
}

func AdminUpdateUserPassword(c *gin.Context) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}
	if !candy.IsValidUsername(request.Username) {
		setErrorCode(c, InvalidUsername)
		return
	}
	if len(request.Password) == 0 {
		setErrorCode(c, InvalidPassword)
		return
	}

	user := model.User{Name: request.Username}
	db := storage.Get()
	if result := db.Model(&model.User{}).Where(user).Take(&user); result.Error != nil {
		setErrorCode(c, Unexpected)
		return
	}
	password := hashUserPassword(user.Name, request.Password)
	err := storage.WriteContext(c.Request.Context(), func(tx *gorm.DB) error {
		result := tx.Model(&model.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{"password": password, "token": "", "token_expires_at": nil})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return businessError(UserNotExists)
		}
		return nil
	})
	if !writeSucceeded(c, err) {
		return
	}
	setResponseData(c, nil)
}

func AdminGetOpenRegisterConfig(c *gin.Context) {
	openreg := model.GetConfig("openreg", "false") == "true"
	setResponseData(c, gin.H{
		"openreg": openreg,
	})
}

func AdminSetOpenRegisterConfig(c *gin.Context) {
	var request struct {
		OpenReg bool `json:"openreg"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}
	if !writeSucceeded(c, model.SetConfig("openreg", strconv.FormatBool(request.OpenReg))) {
		return
	}
	setResponseData(c, nil)
}

func AdminGetRegisterIntervalConfig(c *gin.Context) {
	intervalStr := model.GetConfig("reginterval", "1440")
	interval, err := strconv.Atoi(intervalStr)
	if err != nil {
		interval = 1440
	}
	setResponseData(c, gin.H{
		"reginterval": interval,
	})
}

func AdminSetRegisterIntervalConfig(c *gin.Context) {
	var request struct {
		RegInterval uint `json:"reginterval"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}

	if !writeSucceeded(c, model.SetConfig("reginterval", strconv.FormatUint(uint64(request.RegInterval), 10))) {
		return
	}
	setResponseData(c, nil)
}

func AdminGetAutoCleanUserConfig(c *gin.Context) {
	autoCleanUser := model.GetConfig("autoCleanUser", "false") == "true"
	setResponseData(c, gin.H{
		"autoCleanUser": autoCleanUser,
	})
}

func AdminSetAutoCleanUserConfig(c *gin.Context) {
	var request struct {
		AutoCleanInactiveUser bool `json:"autoCleanUser"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}
	if !writeSucceeded(c, model.SetConfig("autoCleanUser", strconv.FormatBool(request.AutoCleanInactiveUser))) {
		return
	}
	setResponseData(c, nil)
}

func AdminGetInactiveUserThresholdConfig(c *gin.Context) {
	thresholdStr := model.GetConfig("inactiveUserThreshold", "7")
	threshold, err := strconv.Atoi(thresholdStr)
	if err != nil {
		threshold = 7
	}
	setResponseData(c, gin.H{
		"inactiveUserThreshold": threshold,
	})
}

func AdminSetInactiveUserThresholdConfig(c *gin.Context) {
	var request struct {
		InactiveUserThreshold uint `json:"inactiveUserThreshold"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}
	if request.InactiveUserThreshold <= 0 {
		setErrorCode(c, InvalidInactiveUserThreshold)
		return
	}

	if !writeSucceeded(c, model.SetConfig("inactiveUserThreshold", strconv.FormatUint(uint64(request.InactiveUserThreshold), 10))) {
		return
	}
	setResponseData(c, nil)
}

func AdminCleanInactiveUser(c *gin.Context) {
	candy.CleanInactiveUser()
	setResponseData(c, nil)
}
