package main

import (
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/lanthora/cacao/api"
	"github.com/lanthora/cacao/argp"
	"github.com/lanthora/cacao/candy"
	"github.com/lanthora/cacao/frontend"
	"github.com/lanthora/cacao/logger"
	"github.com/lanthora/cacao/util"
)

func init() {
	gin.SetMode(gin.ReleaseMode)
}

func main() {
	r, err := newRouter(argp.Get("trusted-proxies", ""))
	if err != nil {
		logger.Fatal("invalid trusted proxies: %v", err)
	}

	storageDir := argp.Get("storage", ".")
	crtFilename, findCrtErr := util.FindFileByExtFromDir(storageDir, ".crt")
	keyFilename, findKeyErr := util.FindFileByExtFromDir(storageDir, ".key")
	if findCrtErr == nil && findKeyErr == nil {
		addr := argp.Get("listen", ":443")
		logger.Info("listen=[%v]", addr)
		if err := r.RunTLS(addr, path.Join(storageDir, crtFilename), path.Join(storageDir, keyFilename)); err != nil {
			logger.Fatal("tls service run failed: %v", err)
		}
	} else {
		addr := argp.Get("listen", ":80")
		logger.Info("listen=[%v]", addr)
		if err := r.Run(addr); err != nil {
			logger.Fatal("service run failed: %v", err)
		}
	}
}

func newRouter(trustedProxies string) (*gin.Engine, error) {
	r := gin.New()
	var proxies []string
	if strings.TrimSpace(trustedProxies) != "" {
		for _, proxy := range strings.Split(trustedProxies, ",") {
			proxies = append(proxies, strings.TrimSpace(proxy))
		}
	}
	// Forwarded client addresses must only come from explicitly trusted proxies.
	if err := r.SetTrustedProxies(proxies); err != nil {
		return nil, err
	}
	// Gin's default panic logger dumps request headers, including session cookies.
	recovery := gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		logger.Info("http handler panic: method=%s path=%s", c.Request.Method, c.FullPath())
		c.AbortWithStatus(http.StatusInternalServerError)
	})
	r.Use(recovery, candy.WebsocketMiddleware())

	public := r.Group("/api")
	public.POST("/user/register", api.UserRegister)
	public.POST("/user/login", api.UserLogin)
	protected := public.Group("", api.LoginMiddleware(), api.AdminMiddleware())

	admin := protected.Group("/admin")
	admin.POST("/showUsers", api.AdminShowUsers)
	admin.POST("/addUser", api.AdminAddUser)
	admin.POST("/deleteUser", api.AdminDeleteUser)
	admin.POST("/updateUserPassword", api.AdminUpdateUserPassword)
	admin.POST("/getOpenRegisterConfig", api.AdminGetOpenRegisterConfig)
	admin.POST("/setOpenRegisterConfig", api.AdminSetOpenRegisterConfig)
	admin.POST("/getRegisterIntervalConfig", api.AdminGetRegisterIntervalConfig)
	admin.POST("/setRegisterIntervalConfig", api.AdminSetRegisterIntervalConfig)
	admin.POST("/getAutoCleanUserConfig", api.AdminGetAutoCleanUserConfig)
	admin.POST("/setAutoCleanUserConfig", api.AdminSetAutoCleanUserConfig)
	admin.POST("/getInactiveUserThresholdConfig", api.AdminGetInactiveUserThresholdConfig)
	admin.POST("/setInactiveUserThresholdConfig", api.AdminSetInactiveUserThresholdConfig)
	admin.POST("/cleanInactiveUser", api.AdminCleanInactiveUser)

	user := protected.Group("/user")
	user.POST("/info", api.UserInfo)
	user.POST("/statistics", api.UserStatistics)
	user.POST("/changePassword", api.ChangePassword)
	user.POST("/logout", api.UserLogout)

	net := protected.Group("/net")
	net.POST("/show", api.NetShow)
	net.POST("/insert", api.NetInsert)
	net.POST("/edit", api.NetEdit)
	net.POST("/delete", api.NetDelete)

	device := protected.Group("/device")
	device.POST("/show", api.DeviceShow)
	device.POST("/delete", api.DeviceDelete)

	route := protected.Group("/route")
	route.POST("/show", api.RouteShow)
	route.POST("/insert", api.RouteInsert)
	route.POST("/delete", api.RouteDelete)

	r.NoRoute(func(c *gin.Context) {
		if c.Request.URL.Path == "/api" || strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		frontend.Static(c)
	})
	return r, nil
}
