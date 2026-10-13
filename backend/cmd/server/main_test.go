package main

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestReserveRequestValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name    string
		body    string
		wantBad bool
	}{
		{"valid", `{"idempotency_key":"idem_1","user_id":"usr_1","item_id":"item_1","quantity":1}`, false},
		{"missing idempotency key", `{"user_id":"usr_1","item_id":"item_1","quantity":1}`, true},
		{"missing user", `{"item_id":"item_1","quantity":1}`, true},
		{"zero quantity", `{"user_id":"usr_1","item_id":"item_1","quantity":0}`, true},
		{"negative quantity", `{"user_id":"usr_1","item_id":"item_1","quantity":-1}`, true},
		{"invalid json", `{`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req reserveRequest
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/", strings.NewReader(tt.body))
			err := c.ShouldBindJSON(&req)
			if (err != nil) != tt.wantBad {
				t.Fatalf("ShouldBindJSON error=%v, wantBad=%v", err, tt.wantBad)
			}
		})
	}
}
