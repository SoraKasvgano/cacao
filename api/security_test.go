package api

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lanthora/cacao/model"
	"github.com/lanthora/cacao/storage"
)

func securityRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.SetTrustedProxies(nil)
	r.POST("/api/user/login", UserLogin)
	r.POST("/api/user/register", UserRegister)
	protected := r.Group("/api", LoginMiddleware(), AdminMiddleware())
	protected.POST("/user/info", UserInfo)
	protected.POST("/user/logout", UserLogout)
	protected.POST("/user/changePassword", ChangePassword)
	protected.POST("/admin/updateUserPassword", AdminUpdateUserPassword)
	protected.POST("/admin/showUsers", AdminShowUsers)
	protected.POST("/net/delete", NetDelete)
	protected.POST("/device/delete", DeviceDelete)
	protected.POST("/route/delete", RouteDelete)
	return r
}

func securityRequest(t *testing.T, r http.Handler, path, body string, cookies ...*http.Cookie) (*httptest.ResponseRecorder, int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:12345"
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var response struct {
		Status int `json:"status"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("invalid response: %s: %v", w.Body.String(), err)
	}
	return w, response.Status
}

func testUser(t *testing.T, role string) (model.User, []*http.Cookie) {
	t.Helper()
	u := model.User{Name: "test" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20], Role: role}
	digest := sha256.Sum256([]byte(u.Name + ":old-password"))
	u.Password = hex.EncodeToString(digest[:])
	token := newSession(&u)
	if err := storage.Get().Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { storage.Get().Unscoped().Delete(&u) })
	return u, []*http.Cookie{{Name: "id", Value: fmt.Sprint(u.ID)}, {Name: "token", Value: token}}
}

func resetAuthAttempts(t *testing.T) {
	t.Helper()
	authAttempts = &attemptLimiter{entries: make(map[string]attemptWindow)}
}

func TestSessionAuthorization(t *testing.T) {
	r := securityRouter()
	u, cookies := testUser(t, "normal")
	for _, path := range []string{"/api/user/info", "/api/admin/showUsers", "/api/net/delete", "/api/device/delete", "/api/route/delete"} {
		if _, status := securityRequest(t, r, path, "{}"); status != NotLoggedIn {
			t.Fatalf("anonymous %s: %d", path, status)
		}
	}
	if _, status := securityRequest(t, r, "/api/user/info?cache=1", "{}", cookies...); status != Success {
		t.Fatalf("query-string session failed: %d", status)
	}
	if _, status := securityRequest(t, r, "/api/admin/showUsers?cache=1", "{}", cookies...); status != PermissionDenied {
		t.Fatalf("normal user reached admin: %d", status)
	}
	if _, status := securityRequest(t, r, "/api/user/info", "{}", &http.Cookie{Name: "id", Value: "0"}, cookies[1]); status != NotLoggedIn {
		t.Fatal("zero user ID accepted")
	}
	if _, status := securityRequest(t, r, "/api/user/info", "{}", cookies[0], &http.Cookie{Name: "token", Value: u.Token}); status != NotLoggedIn {
		t.Fatal("database token digest accepted as cookie")
	}
	storage.Get().Model(&u).Update("token_expires_at", time.Now().Add(-time.Second))
	if _, status := securityRequest(t, r, "/api/user/info", "{}", cookies...); status != NotLoggedIn {
		t.Fatal("expired session accepted")
	}
}

func TestLegacySessionUpgradeAndLogout(t *testing.T) {
	r := securityRouter()
	u, cookies := testUser(t, "normal")
	storage.Get().Model(&u).Update("token", cookies[1].Value)
	if _, status := securityRequest(t, r, "/api/user/info", "{}", cookies...); status != Success {
		t.Fatalf("legacy session rejected: %d", status)
	}
	storage.Get().First(&u, u.ID)
	if !strings.HasPrefix(u.Token, "sha256:") {
		t.Fatal("legacy token not migrated")
	}
	if _, status := securityRequest(t, r, "/api/user/logout", "{}", cookies...); status != Success {
		t.Fatalf("logout failed: %d", status)
	}
	model.RefreshUserLastActiveTimeByUserID(u.ID)
	if _, status := securityRequest(t, r, "/api/user/info", "{}", cookies...); status != NotLoggedIn {
		t.Fatal("logout token survived activity refresh")
	}
}

func TestLegacyPasswordUpgradeAndRevocation(t *testing.T) {
	resetAuthAttempts(t)
	r := securityRouter()
	u, oldCookies := testUser(t, "normal")
	w, status := securityRequest(t, r, "/api/user/login", fmt.Sprintf(`{"username":%q,"password":"old-password"}`, u.Name))
	if status != Success {
		t.Fatalf("legacy login failed: %d", status)
	}
	if len(authAttempts.entries) != 0 {
		t.Fatal("successful login consumed the failed-authentication budget")
	}
	storage.Get().First(&u, u.ID)
	if !strings.HasPrefix(u.Password, "bcrypt-sha256:") || !verifyUserPassword(&u, "old-password") {
		t.Fatal("legacy password migration failed")
	}
	if _, status := securityRequest(t, r, "/api/user/info", "{}", oldCookies...); status != NotLoggedIn {
		t.Fatal("login did not rotate token")
	}
	cookies := w.Result().Cookies()
	for _, cookie := range cookies {
		if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
			t.Fatalf("unsafe cookie: %s", cookie.Name)
		}
	}
	if _, status := securityRequest(t, r, "/api/user/info", "{}", cookies...); status != Success {
		t.Fatal("new login cookies rejected")
	}
	_, adminCookies := testUser(t, "admin")
	if _, status := securityRequest(t, r, "/api/admin/updateUserPassword", fmt.Sprintf(`{"username":%q,"password":"new-password"}`, u.Name), adminCookies...); status != Success {
		t.Fatalf("password reset failed: %d", status)
	}
	if _, status := securityRequest(t, r, "/api/user/info", "{}", cookies...); status != NotLoggedIn {
		t.Fatal("reset did not revoke old session")
	}
	if _, status := securityRequest(t, r, "/api/user/login", fmt.Sprintf(`{"username":%q,"password":"old-password"}`, u.Name)); status != IncorrectUsernameOrPassword {
		t.Fatal("old password accepted")
	}
}

func TestPasswordChangeForAdminAndLongPasswords(t *testing.T) {
	resetAuthAttempts(t)
	r := securityRouter()
	u, cookies := testUser(t, "admin")
	password := strings.Repeat("long-password", 20)
	w, status := securityRequest(t, r, "/api/user/changePassword", fmt.Sprintf(`{"old":"old-password","new":%q}`, password), cookies...)
	if status != Success {
		t.Fatalf("admin password change failed: %d", status)
	}
	storage.Get().First(&u, u.ID)
	if !verifyUserPassword(&u, password) || verifyUserPassword(&u, password+"x") {
		t.Fatal("long password truncated")
	}
	if _, status := securityRequest(t, r, "/api/user/info", "{}", cookies...); status != NotLoggedIn {
		t.Fatal("password change kept old token")
	}
	if _, status := securityRequest(t, r, "/api/user/info", "{}", w.Result().Cookies()...); status != Success {
		t.Fatal("new session rejected")
	}
}

func TestAuthenticationRateLimit(t *testing.T) {
	resetAuthAttempts(t)
	r := securityRouter()
	for i := 0; i < 30; i++ {
		_, status := securityRequest(t, r, "/api/user/login", `{"username":"missing-user","password":"wrong"}`)
		if status != IncorrectUsernameOrPassword {
			t.Fatalf("attempt %d: %d", i, status)
		}
	}
	w, status := securityRequest(t, r, "/api/user/login", `{"username":"missing-user","password":"wrong"}`)
	if w.Code != http.StatusTooManyRequests || status != TooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatal("brute force not throttled")
	}
	_, cookies := testUser(t, "normal")
	if _, status := securityRequest(t, r, "/api/user/info", "{}", cookies...); status != Success {
		t.Fatal("authentication limiter disrupted business API")
	}
	now := time.Now()
	l := &attemptLimiter{entries: map[string]attemptWindow{}}
	if !l.allow("a", 1, time.Minute, now) || l.allow("a", 1, time.Minute, now) || !l.allow("a", 1, time.Minute, now.Add(time.Minute)) {
		t.Fatal("limiter expiry failed")
	}
}

func TestSetupRequiresSecret(t *testing.T) {
	resetAuthAttempts(t)
	t.Setenv("CACAO_SETUP_TOKEN", "")
	if validSetupToken("") {
		t.Fatal("unconfigured setup accepted")
	}
	secret := strings.Repeat("s", 32)
	t.Setenv("CACAO_SETUP_TOKEN", secret)
	if validSetupToken("wrong") || !validSetupToken(secret) {
		t.Fatal("setup secret comparison failed")
	}
	var count int64
	storage.Get().Unscoped().Model(&model.User{}).Count(&count)
	if count != 0 {
		t.Skip("bootstrap endpoint test needs a fresh test database")
	}
	r := securityRouter()
	if _, status := securityRequest(t, r, "/api/user/register", `{"username":"attacker","password":"password"}`); status != SetupRequired {
		t.Fatal("anonymous first user can claim admin")
	}
	previousRegistration := model.GetConfig("openreg", "missing")
	t.Cleanup(func() {
		storage.Get().Unscoped().Where("name = ?", "bootstrap").Delete(&model.User{})
		if previousRegistration == "missing" {
			storage.Get().Unscoped().Where("key = ?", "openreg").Delete(&model.Config{})
		} else {
			model.SetConfig("openreg", previousRegistration)
		}
	})
	w, status := securityRequest(t, r, "/api/user/register", fmt.Sprintf(`{"username":"bootstrap","password":"password","setupToken":%q}`, secret))
	if status != Success {
		t.Fatalf("authorized setup rejected: %d", status)
	}
	var admin model.User
	if storage.Get().Where("name = ?", "bootstrap").First(&admin).Error != nil || admin.Role != "admin" {
		t.Fatal("authorized setup did not create administrator")
	}
	if _, status := securityRequest(t, r, "/api/user/info", "{}", w.Result().Cookies()...); status != Success {
		t.Fatal("bootstrap login session rejected")
	}
	if _, status := securityRequest(t, r, "/api/user/register", fmt.Sprintf(`{"username":"attacker","password":"password","setupToken":%q}`, secret)); status != RegistrationDisabled {
		t.Fatal("setup secret remained usable after initialization")
	}
}

func TestCookiesSecureWithTLS(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "https://example.test/", nil)
	c.Request.TLS = &tls.ConnectionState{}
	setSessionCookies(c, 1, "test-token", 86400)
	for _, value := range c.Writer.Header().Values("Set-Cookie") {
		if !strings.Contains(value, "Secure") || !strings.Contains(value, "HttpOnly") || !strings.Contains(value, "SameSite=Lax") {
			t.Fatalf("unsafe TLS cookie: %s", value)
		}
	}
}

func TestCrossTenantAndZeroIDMutations(t *testing.T) {
	r := securityRouter()
	owner, _ := testUser(t, "normal")
	_, cookies := testUser(t, "normal")
	network := model.Net{UserID: owner.ID, Name: "test-net"}
	storage.Get().Create(&network)
	t.Cleanup(func() { storage.Get().Unscoped().Delete(&network) })
	device := model.Device{NetID: network.ID, Online: true}
	storage.Get().Create(&device)
	t.Cleanup(func() { storage.Get().Unscoped().Delete(&device) })
	route := model.Route{NetID: network.ID}
	storage.Get().Create(&route)
	t.Cleanup(func() { storage.Get().Unscoped().Delete(&route) })
	for _, item := range []struct {
		path, body string
		status     int
	}{
		{"/api/net/delete", fmt.Sprintf(`{"netid":%d}`, network.ID), NetworkNotExists},
		{"/api/net/delete", `{"netid":0}`, NetworkNotExists},
		{"/api/device/delete", fmt.Sprintf(`{"devid":%d}`, device.ID), DeviceNotExists},
		{"/api/route/delete", fmt.Sprintf(`{"routeid":%d}`, route.ID), RouteNotExists},
	} {
		if _, status := securityRequest(t, r, item.path, item.body, cookies...); status != item.status {
			t.Fatalf("%s: got %d want %d", item.path, status, item.status)
		}
	}
	storage.Get().Delete(&network)
	if model.GetNetByNetID(network.ID).ID != 0 {
		t.Fatal("deleted network still accessible")
	}
}
