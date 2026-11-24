//go:build e2e

/*
Copyright 2026 DragonflyDB authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const endpoint = "http://127.0.0.1:18080"

func TestAuthSidecarE2E(t *testing.T) {
	client := &http.Client{Timeout: 10 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantBody   string
	}{
		{"alice with two groups uses first mapping", `{"username":"alice","password":"alice-demo"}`, http.StatusOK, `{"valid":true,"username":"writer"}`},
		{"bob", `{"username":"bob","password":"bob-demo"}`, http.StatusOK, `{"valid":true,"username":"reader"}`},
		{"carol without groups", `{"username":"carol","password":"carol-demo"}`, http.StatusUnauthorized, `{"valid":false}`},
		{"wrong password", `{"username":"alice","password":"wrong"}`, http.StatusOK, `{"valid":false}`},
		{"another user's password", `{"username":"bob","password":"alice-demo"}`, http.StatusOK, `{"valid":false}`},
		{"unknown user", `{"username":"nobody","password":"alice-demo"}`, http.StatusUnauthorized, `{"valid":false}`},
		{"literal wildcard", `{"username":"alice*","password":"alice-demo"}`, http.StatusUnauthorized, `{"valid":false}`},
		{"LDAP filter characters", `{"username":"alice)(uid=*)","password":"alice-demo"}`, http.StatusUnauthorized, `{"valid":false}`},
		{"empty username", `{"username":"","password":"alice-demo"}`, http.StatusBadRequest, `{"valid":false}`},
		{"empty password", `{"username":"alice","password":""}`, http.StatusBadRequest, `{"valid":false}`},
		{"missing username", `{"password":"alice-demo"}`, http.StatusBadRequest, `{"valid":false}`},
		{"missing password", `{"username":"alice"}`, http.StatusBadRequest, `{"valid":false}`},
		{"empty object", `{}`, http.StatusBadRequest, `{"valid":false}`},
		{"null credentials", `{"username":null,"password":null}`, http.StatusBadRequest, `{"valid":false}`},
		{"null body", `null`, http.StatusBadRequest, `{"valid":false}`},
		{"malformed JSON", `{`, http.StatusBadRequest, `{"valid":false}`},
		{"non-string username", `{"username":1,"password":"alice-demo"}`, http.StatusBadRequest, `{"valid":false}`},
		{"multiple JSON values", `{"username":"alice","password":"alice-demo"}{}`, http.StatusBadRequest, `{"valid":false}`},
		{"trailing garbage", `{"username":"alice","password":"alice-demo"}garbage`, http.StatusBadRequest, `{"valid":false}`},
		{"oversized body", `{"username":"alice","password":"` + strings.Repeat("x", 16384) + `"}`, http.StatusBadRequest, `{"valid":false}`},
		{"valid after rejection", `{"username":"alice","password":"alice-demo"}`, http.StatusOK, `{"valid":true,"username":"writer"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := client.Post(endpoint+"/validate", "application/json", strings.NewReader(tt.body))
			if err != nil {
				t.Fatalf("POST /validate: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", response.StatusCode, tt.wantStatus)
			}
			if got := response.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("read validation response: %v", err)
			}
			if got := strings.TrimSpace(string(body)); got != tt.wantBody {
				t.Errorf("response = %q, want %q", got, tt.wantBody)
			}
		})
	}
}
