package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lanthora/cacao/api"
	"github.com/lanthora/cacao/argp"
	"github.com/lanthora/cacao/candy"
	"github.com/lanthora/cacao/frontend"
	"github.com/lanthora/cacao/logger"
	"github.com/lanthora/cacao/model"
	"github.com/lanthora/cacao/storage"
	"github.com/lanthora/cacao/util"
)

func init() {
	gin.SetMode(gin.ReleaseMode)
}

func main() {
	if err := run(); err != nil {
		logger.Fatal("service failed: %v", err)
	}
}

func run() (result error) {
	if err := argp.MaintainConfig(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := model.EnsureIntegrity(); err != nil {
		return err
	}
	tasks, err := newBackgroundTasks()
	if err != nil {
		return err
	}
	if err := tasks.Start(ctx); err != nil {
		return err
	}
	api.SetBackgroundTasks(tasks)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result = errors.Join(result, tasks.Stop(shutdownCtx))
		result = errors.Join(result, candy.ShutdownConnections(shutdownCtx))
		result = errors.Join(result, storage.Shutdown(shutdownCtx))
	}()
	r, err := newRouter(argp.Get("trusted-proxies", ""))
	if err != nil {
		return err
	}

	storageDir := argp.Get("storage", ".")
	crtFilename, findCrtErr := util.FindFileByExtFromDir(storageDir, ".crt")
	keyFilename, findKeyErr := util.FindFileByExtFromDir(storageDir, ".key")
	if findCrtErr == nil && findKeyErr == nil {
		addr := argp.Get("listen", ":443")
		logger.Info("listen=[%v]", addr)
		return serve(ctx, newHTTPServer(addr, r), path.Join(storageDir, crtFilename), path.Join(storageDir, keyFilename))
	} else {
		addr := argp.Get("listen", ":80")
		logger.Info("listen=[%v]", addr)
		return serve(ctx, newHTTPServer(addr, r), "", "")
	}
}

func serve(ctx context.Context, server *http.Server, cert, key string) error {
	done := make(chan error, 1)
	go func() {
		if cert != "" {
			done <- server.ListenAndServeTLS(cert, key)
		} else {
			done <- server.ListenAndServe()
		}
	}()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
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
	r.Use(recovery, securityHeaders(), candy.WebsocketMiddleware())

	public := r.Group("/api", apiRequestSecurity())
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
	admin.POST("/backgroundTasks", api.AdminBackgroundTasks)
	admin.POST("/runBackgroundTask", api.AdminRunBackgroundTask)

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

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if c.Request.TLS != nil {
			c.Header("Strict-Transport-Security", "max-age=31536000")
		}
		c.Next()
	}
}

const maxAPIRequestBytes = 1 << 20

func apiRequestSecurity() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		if strings.EqualFold(c.GetHeader("Sec-Fetch-Site"), "cross-site") {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if origin := c.GetHeader("Origin"); origin != "" && !sameOriginHost(origin, c.Request.Host) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if c.Request.ContentLength > maxAPIRequestBytes {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		if c.Request.Body != nil {
			body := http.MaxBytesReader(c.Writer, c.Request.Body, maxAPIRequestBytes)
			data, err := io.ReadAll(body)
			body.Close()
			if err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					c.AbortWithStatus(http.StatusRequestEntityTooLarge)
				} else {
					c.AbortWithStatus(http.StatusBadRequest)
				}
				return
			}
			// Existing read-only calls and logout use an empty axios POST body.
			if len(data) != 0 {
				contentType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
				if err != nil || contentType != "application/json" {
					c.AbortWithStatus(http.StatusUnsupportedMediaType)
					return
				}
			}
			c.Request.Body = io.NopCloser(bytes.NewReader(data))
		}
		c.Next()
	}
}

func sameOriginHost(origin, host string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	// Compare against the preserved Host header, including behind TLS termination.
	// Scheme cannot be inferred from untrusted X-Forwarded-Proto headers.
	defaultPort := ":80"
	if parsed.Scheme == "https" {
		defaultPort = ":443"
	}
	originHost := strings.TrimSuffix(strings.ToLower(parsed.Host), defaultPort)
	requestHost := strings.TrimSuffix(strings.ToLower(host), defaultPort)
	return originHost == requestHost
}
