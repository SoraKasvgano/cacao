package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
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
