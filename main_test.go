package main

import (
	"encoding/json"
	"io"
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

func TestAPIRequestSecurity(t *testing.T) {
	for _, test := range []struct {
		name        string
		body        string
		contentType string
		origin      string
		fetchSite   string
		chunked     bool
		want        int
	}{
		{name: "same origin JSON", body: `{}`, contentType: "application/json; charset=utf-8", origin: "https://panel.example", want: http.StatusOK},
		{name: "non-browser JSON", body: `{}`, contentType: "application/json", want: http.StatusOK},
		{name: "empty axios POST", contentType: "application/x-www-form-urlencoded", origin: "https://panel.example", want: http.StatusOK},
		{name: "cross origin", body: `{}`, contentType: "application/json", origin: "https://attacker.example", want: http.StatusForbidden},
		{name: "null origin", origin: "null", want: http.StatusForbidden},
		{name: "cross site metadata", fetchSite: "cross-site", want: http.StatusForbidden},
		{name: "form request", body: "id=1", contentType: "application/x-www-form-urlencoded", want: http.StatusUnsupportedMediaType},
		{name: "plain JSON CSRF", body: `{}`, contentType: "text/plain", want: http.StatusUnsupportedMediaType},
		{name: "missing content type", body: `{}`, want: http.StatusUnsupportedMediaType},
		{name: "oversized body", body: strings.Repeat("x", maxAPIRequestBytes+1), contentType: "application/json", want: http.StatusRequestEntityTooLarge},
		{name: "oversized chunked body", body: strings.Repeat("x", maxAPIRequestBytes+1), contentType: "application/json", chunked: true, want: http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.POST("/api/test", apiRequestSecurity(), func(c *gin.Context) {
				body, err := io.ReadAll(c.Request.Body)
				if err != nil || string(body) != test.body {
					t.Errorf("request body changed: %q, %v", body, err)
				}
				c.Status(http.StatusOK)
			})
			request := httptest.NewRequest(http.MethodPost, "http://panel.example/api/test", strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("Origin", test.origin)
			request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			if test.chunked {
				request.ContentLength = -1
				request.TransferEncoding = []string{"chunked"}
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("HTTP %d, want %d", response.Code, test.want)
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("API response may be cached")
			}
		})
	}
}

func TestSameOriginHost(t *testing.T) {
	for _, test := range []struct {
		origin string
		host   string
		want   bool
	}{
		{"https://panel.example", "panel.example", true},
		{"https://PANEL.example:443", "panel.example", true},
		{"http://panel.example", "panel.example:80", true},
		{"https://panel.example:8443", "panel.example:8443", true},
		{"https://[::1]:8443", "[::1]:8443", true},
		{"https://panel.example:8443", "panel.example", false},
		{"https://panel.example.attacker.example", "panel.example", false},
		{"https://attacker.example@panel.example", "panel.example", false},
		{"https://panel.example/path", "panel.example", false},
		{"https://panel.example?x=1", "panel.example", false},
		{"file://panel.example", "panel.example", false},
		{"null", "panel.example", false},
	} {
		if got := sameOriginHost(test.origin, test.host); got != test.want {
			t.Errorf("sameOriginHost(%q, %q)=%v, want %v", test.origin, test.host, got, test.want)
		}
	}
}

func TestSecurityHeadersAndUnknownAPI(t *testing.T) {
	router, err := newRouter("")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://panel.example/api/unknown", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "<html") {
		t.Fatal("unknown API route must not serve the application page")
	}
	for key, want := range map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Strict-Transport-Security": "max-age=31536000",
	} {
		if got := response.Header().Get(key); got != want {
			t.Errorf("%s=%q, want %q", key, got, want)
		}
	}
	request = httptest.NewRequest(http.MethodGet, "http://panel.example/api/unknown", nil)
	request.Header.Set("X-Forwarded-Proto", "https")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("untrusted forwarded scheme must not enable HSTS")
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
