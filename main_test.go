package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestEveryPrivateAPIRouteRequiresAuthentication(t *testing.T) {
	router, err := newRouter("")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, route := range router.Routes() {
		if route.Path == "/api/user/login" || route.Path == "/api/user/register" {
			continue
		}
		checked++
		t.Run(route.Path, func(t *testing.T) {
			// Query strings must never affect the authorization policy.
			request := httptest.NewRequest(route.Method, route.Path+"?next=/api/user/login", nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var body struct {
				Status int `json:"status"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid authentication response: %v (%s)", err, response.Body.String())
			}
			if body.Status != 2 {
				t.Fatalf("anonymous request reached private route: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	if checked == 0 {
		t.Fatal("no private API routes were checked")
	}
}

func TestClientIPOnlyTrustsConfiguredProxies(t *testing.T) {
	for _, test := range []struct {
		name    string
		proxies string
		want    string
	}{
		{"direct request", "", "192.0.2.10"},
		{"untrusted proxy", "198.51.100.0/24", "192.0.2.10"},
		{"trusted proxy", "192.0.2.0/24", "203.0.113.4"},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, err := newRouter(test.proxies)
			if err != nil {
				t.Fatal(err)
			}
			router.GET("/client-ip-test", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })
			request := httptest.NewRequest(http.MethodGet, "/client-ip-test", nil)
			request.RemoteAddr = "192.0.2.10:12345"
			request.Header.Set("X-Forwarded-For", "203.0.113.4")
			request.Header.Set("X-Real-IP", "203.0.113.5")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if got := response.Body.String(); got != test.want {
				t.Fatalf("ClientIP=%q, want %q", got, test.want)
			}
		})
	}
	if _, err := newRouter("not-a-proxy"); err == nil {
		t.Fatal("invalid trusted proxy must fail startup")
	}
}

func TestPanicRecoveryDoesNotExposeDetails(t *testing.T) {
	router, err := newRouter("")
	if err != nil {
		t.Fatal(err)
	}
	router.GET("/panic-test", func(c *gin.Context) { panic("private panic details") })
	request := httptest.NewRequest(http.MethodGet, "/panic-test", nil)
	request.Header.Set("Cookie", "token=private-session-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "private") {
		t.Fatalf("unexpected panic response: %d %s", response.Code, response.Body.String())
	}
}
