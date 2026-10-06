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

func LoginMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		idstr, errid := c.Cookie("id")
		token, errtoken := c.Cookie("token")
		if errid != nil || errtoken != nil || len(idstr) == 0 || len(token) == 0 {
			setErrorCode(c, NotLoggedIn)
			c.Abort()
			return
		}
		id, err := strconv.ParseUint(idstr, 10, 64)
		if err != nil || id == 0 || uint64(uint(id)) != id || len(token) > 128 {
			setErrorCode(c, NotLoggedIn)
			c.Abort()
			return
		}
		user := &model.User{}
		user.ID = uint(id)

		db := storage.Get()
		result := db.Where("id = ?", id).Take(user)
		if result.Error != nil || !validSessionToken(user.Token, token) || user.TokenExpiresAt == nil || !time.Now().Before(*user.TokenExpiresAt) {
			setErrorCode(c, NotLoggedIn)
			c.Abort()
			return
		}
		if !strings.HasPrefix(user.Token, "sha256:") {
			// Upgrade stored legacy tokens without changing the browser cookie.
			var result *gorm.DB
			err := storage.WriteContext(c.Request.Context(), func(tx *gorm.DB) error {
				result = tx.Model(&model.User{}).Where("id = ? AND token = ?", user.ID, user.Token).UpdateColumn("token", hashSessionToken(token))
				return result.Error
			})
			if err != nil {
				setErrorCode(c, Unexpected)
				c.Abort()
				return
			}
			if result.RowsAffected == 0 {
				// Another request may have migrated or revoked this session.
				if db.Where("id = ?", id).Take(user).Error != nil || !validSessionToken(user.Token, token) || user.TokenExpiresAt == nil || !time.Now().Before(*user.TokenExpiresAt) {
					setErrorCode(c, NotLoggedIn)
					c.Abort()
					return
				}
			} else {
				user.Token = hashSessionToken(token)
			}
		}
		c.Set("user", user)
		c.Next()
	}
}

func UserInfo(c *gin.Context) {
	user := c.MustGet("user").(*model.User)
	setResponseData(c, gin.H{
		"name":    user.Name,
		"role":    user.Role,
		"regtime": user.CreatedAt.Format(time.DateTime),
	})
}

func UserStatistics(c *gin.Context) {
	user := c.MustGet("user").(*model.User)
	statistics, err := model.GetUserStatistics(user.ID)
	if !writeSucceeded(c, err) {
		return
	}
	setResponseData(c, gin.H{
		"netnum": statistics.NetNum,
		"devnum": statistics.DevNum,
		"rxsum":  statistics.RxSum,
		"txsum":  statistics.TxSum,
	})
}

func UserRegister(c *gin.Context) {
	var request struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		SetupToken string `json:"setupToken"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}
	if !allowAuthentication(c, request.Username) {
		return
	}
	db := storage.Get()
	var count int64
	if err := db.Unscoped().Model(&model.User{}).Count(&count).Error; err != nil {
		setErrorCode(c, Unexpected)
		return
	}
	initialSetup := count == 0
	if initialSetup {
		if !validSetupToken(request.SetupToken) {
			recordAuthenticationFailure(c, request.Username)
			setErrorCode(c, SetupRequired)
			return
		}
	} else if model.GetConfig("openreg", "false") != "true" {
		setErrorCode(c, RegistrationDisabled)
		return
	}
	if request.Username == "@" {
		setErrorCode(c, InvalidUsername)
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
	if !acquirePasswordWork(c) {
		return
	}
	defer func() { <-passwordWork }()

	role := "normal"
	if initialSetup {
		role = "admin"
	}

	user := model.User{
		Name:     request.Username,
		Password: hashUserPassword(request.Username, request.Password),
		Role:     role,
		IP:       c.ClientIP(),
	}
	token := newSession(&user)
	netModel := &model.Net{Name: "@", Password: randomString(8), DHCP: "192.168.202.0/24", Broadcast: true}
	if !writeSucceeded(c, createAccount(c, &user, netModel, true, validSetupToken(request.SetupToken))) {
		return
	}
	if user.Role == "normal" {
		if !writeSucceeded(c, candy.SyncNet(netModel.ID)) {
			return
		}
	}

	setSessionCookies(c, user.ID, token, int(sessionLifetime.Seconds()))

	setResponseData(c, gin.H{
		"name": user.Name,
		"role": user.Role,
	})

}

func UserLogin(c *gin.Context) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}

	if !allowAuthentication(c, request.Username) {
		return
	}
	if !candy.IsValidUsername(request.Username) || request.Password == "" {
		recordAuthenticationFailure(c, request.Username)
		setErrorCode(c, IncorrectUsernameOrPassword)
		return
	}
	user := model.User{}
	if !acquirePasswordWork(c) {
		return
	}
	defer func() { <-passwordWork }()

	db := storage.Get()

	if result := db.Where("name = ?", request.Username).Take(&user); result.Error != nil || !verifyUserPassword(&user, request.Password) {
		recordAuthenticationFailure(c, request.Username)
		setErrorCode(c, IncorrectUsernameOrPassword)
		return
	}

	if len(user.IP) == 0 {
		user.IP = c.ClientIP()
	}

	previousPassword := user.Password
	if !strings.HasPrefix(user.Password, "bcrypt-sha256:") {
		user.Password = hashUserPassword(user.Name, request.Password)
	}
	token := newSession(&user)
	var result *gorm.DB
	err := storage.WriteContext(c.Request.Context(), func(tx *gorm.DB) error {
		result = tx.Model(&model.User{}).Where("id = ? AND password = ?", user.ID, previousPassword).Updates(map[string]interface{}{
			"password": user.Password, "token": user.Token, "token_expires_at": user.TokenExpiresAt, "ip": user.IP,
		})
		return result.Error
	})
	if !writeSucceeded(c, err) {
		return
	}
	if result.RowsAffected != 1 {
		setErrorCode(c, IncorrectUsernameOrPassword)
		return
	}
	setSessionCookies(c, user.ID, token, int(sessionLifetime.Seconds()))

	setResponseData(c, gin.H{
		"name": user.Name,
		"role": user.Role,
	})
}

func UserLogout(c *gin.Context) {
	user := c.MustGet("user").(*model.User)
	err := storage.WriteContext(c.Request.Context(), func(tx *gorm.DB) error {
		return tx.Model(&model.User{}).Where("id = ? AND token = ?", user.ID, user.Token).Updates(map[string]interface{}{"token": "", "token_expires_at": nil}).Error
	})
	if !writeSucceeded(c, err) {
		return
	}
	setSessionCookies(c, 0, "", -1)

	setResponseData(c, nil)
}

func ChangePassword(c *gin.Context) {
	var request struct {
		OldPassword string `json:"old"`
		NewPassword string `json:"new"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		setErrorCode(c, InvalidRequest)
		return
	}

	user := c.MustGet("user").(*model.User)
	if !allowAuthentication(c, user.Name) {
		return
	}
	if !acquirePasswordWork(c) {
		return
	}
	defer func() { <-passwordWork }()
	if !verifyUserPassword(user, request.OldPassword) {
		recordAuthenticationFailure(c, user.Name)
		setErrorCode(c, IncorrectUsernameOrPassword)
		return
	}
	if len(request.NewPassword) == 0 {
		setErrorCode(c, InvalidPassword)
		return
	}

	previousPassword, previousToken := user.Password, user.Token
	user.Password = hashUserPassword(user.Name, request.NewPassword)
	token := newSession(user)
	var result *gorm.DB
	err := storage.WriteContext(c.Request.Context(), func(tx *gorm.DB) error {
		result = tx.Model(&model.User{}).Where("id = ? AND password = ? AND token = ?", user.ID, previousPassword, previousToken).Updates(map[string]interface{}{
			"password": user.Password, "token": user.Token, "token_expires_at": user.TokenExpiresAt,
		})
		return result.Error
	})
	if !writeSucceeded(c, err) {
		return
	}
	if result.RowsAffected != 1 {
		setErrorCode(c, NotLoggedIn)
		return
	}
	setSessionCookies(c, user.ID, token, int(sessionLifetime.Seconds()))

	setResponseData(c, nil)
}

func registerInterval() time.Duration {
	intervalStr := model.GetConfig("reginterval", "1440")
	interval, err := strconv.Atoi(intervalStr)
	if err != nil {
		interval = 1440
	}
	return time.Duration(interval) * time.Minute
}
