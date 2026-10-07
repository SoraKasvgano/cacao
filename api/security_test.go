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
