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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-ldap/ldap/v3"
)

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type result struct {
	Valid    bool   `json:"valid"`
	Username string `json:"username,omitempty"`
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	server := newServer(cfg)

	go func() {
		if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server forced to shutdown", "error", err)
			os.Exit(1)
		}
	}()

	gracefulShutdown(server, cfg.ShutdownTimeout)
}

func newServer(cfg config) *http.Server {
	return &http.Server{
		Addr: fmt.Sprintf(":%s", cfg.Port),
		Handler: handler(func(ctx context.Context, username, password string) (string, error) {
			return search(ctx, cfg, username, password)
		}),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}
}

func handler(authenticate func(context.Context, string, string) (string, error)) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /validate", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		var input credentials
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))

		if err := dec.Decode(&input); err != nil || dec.Decode(&struct{}{}) != io.EOF {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(result{})
			return
		}

		var res result // Defaults to {Valid: false, Username: ""}

		if user, err := authenticate(r.Context(), input.Username, input.Password); err == nil {
			res = result{Valid: true, Username: user}
		} else if !ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			log.Printf("LDAP authentication failed: %v", err)
		}
		json.NewEncoder(w).Encode(res)
	})
	return mux
}

func parseEndpoint(ldapURL string) (*url.URL, error) {
	endpoint, err := url.Parse(ldapURL)
	if err != nil || endpoint.Hostname() == "" {
		return nil, errors.New("LDAP endpoint must include a valid host")
	}
	switch endpoint.Scheme {
	case "ldap", "ldaps":
		return endpoint, nil
	default:
		return nil, errors.New("LDAP endpoint must use ldap:// or ldaps://")
	}
}

func gracefulShutdown(server *http.Server, timeout time.Duration) {
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	sig := <-quit
	slog.Info("shutting down server", "signal", sig.String())

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		slog.Error("server forced to shutdown", "error", err)
		os.Exit(1)
	}

	slog.Info("server stopped gracefully")
}
