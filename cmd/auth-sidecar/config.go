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
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/go-ldap/ldap/v3"
)

type searchScope string

const (
	scopeBase searchScope = "base"
	scopeOne  searchScope = "one"
	scopeSub  searchScope = "sub"
)

func (scope searchScope) ldapScope() (int, error) {
	switch scope {
	case scopeBase:
		return ldap.ScopeBaseObject, nil
	case scopeOne:
		return ldap.ScopeSingleLevel, nil
	case scopeSub:
		return ldap.ScopeWholeSubtree, nil
	default:
		return 0, fmt.Errorf("LDAP_SEARCH_SCOPE must be base, one or sub, got %q", scope)
	}
}

type config struct {
	httpConfig
	ldapConfig
}

type httpConfig struct {
	Port              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

type ldapConfig struct {
	// general
	Servers              []string // TODO: Implement support for multiple LDAP servers.
	TimeoutConnection    time.Duration
	TimeoutLDAPOperation int

	// tls
	UseStartTLS bool
	TLSCACert   string
	TLSCert     string
	TLSKey      string

	// search+bind
	SearchBindDN       string
	SearchBindPassword string
	SearchBaseDN       string
	SearchFilter       string
	SearchAttribute    string
	SearchDNAttribute  string
	SearchScope        searchScope
}

func loadConfig() (config, error) {
	cfg := config{
		httpConfig: httpConfig{
			Port:              getEnvString("HTTP_PORT", "8080"),
			ReadHeaderTimeout: time.Duration(getEnvInt("HTTP_READ_HEADER_TIMEOUT", 5)) * time.Second,
			ReadTimeout:       time.Duration(getEnvInt("HTTP_READ_TIMEOUT", 10)) * time.Second,
			WriteTimeout:      time.Duration(getEnvInt("HTTP_WRITE_TIMEOUT", 10)) * time.Second,
			IdleTimeout:       time.Duration(getEnvInt("HTTP_IDLE_TIMEOUT", 10)) * time.Second,
			ShutdownTimeout:   time.Duration(getEnvInt("HTTP_SHUTDOWN_TIMEOUT", 10)) * time.Second,
		},
		ldapConfig: ldapConfig{
			//general
			Servers:              []string{os.Getenv("LDAP_SERVERS")},
			TimeoutConnection:    time.Duration(getEnvInt("LDAP_TIMEOUT_CONNECTION", 10)) * time.Second,
			TimeoutLDAPOperation: getEnvInt("LDAP_TIMEOUT_LDAP_OPERATION", 10),

			// tls
			UseStartTLS: getEnvBool("LDAP_USE_STARTTLS", false),
			TLSCACert:   os.Getenv("LDAP_TLS_CA_CERT"),
			TLSCert:     os.Getenv("LDAP_TLS_CERT"),
			TLSKey:      os.Getenv("LDAP_TLS_KEY"),

			// search+bind
			SearchBindDN:       os.Getenv("LDAP_SEARCH_BIND_DN"),
			SearchBindPassword: os.Getenv("LDAP_SEARCH_BIND_PASSWORD"),
			SearchBaseDN:       os.Getenv("LDAP_SEARCH_BASE_DN"),
			SearchFilter:       getEnvString("LDAP_SEARCH_FILTER", "(objectClass=*)"),
			SearchAttribute:    getEnvString("LDAP_SEARCH_ATTRIBUTE", "uid"),
			SearchDNAttribute:  getEnvString("LDAP_SEARCH_DN_ATTRIBUTE", "entryDN"),
			SearchScope:        searchScope(getEnvString("LDAP_SEARCH_SCOPE", string(scopeSub))),
		},
	}
	return cfg, cfg.validate()
}

func (cfg config) validate() error {
	// http config
	if port, err := strconv.ParseUint(cfg.Port, 10, 16); err != nil || port == 0 {
		return errors.New("HTTP_PORT must be an integer between 1 and 65535")
	}
	if cfg.ReadHeaderTimeout <= 0 {
		return errors.New("HTTP_READ_HEADER_TIMEOUT must be greater than zero")
	}
	if cfg.ReadTimeout <= 0 {
		return errors.New("HTTP_READ_TIMEOUT must be greater than zero")
	}
	if cfg.WriteTimeout <= 0 {
		return errors.New("HTTP_WRITE_TIMEOUT must be greater than zero")
	}
	if cfg.IdleTimeout <= 0 {
		return errors.New("HTTP_IDLE_TIMEOUT must be greater than zero")
	}
	if cfg.ShutdownTimeout <= 0 {
		return errors.New("HTTP_SHUTDOWN_TIMEOUT must be greater than zero")
	}

	// ldap config

	// general
	if len(cfg.Servers) != 1 {
		return errors.New("LDAP_SERVERS must contain exactly one endpoint")
	}
	if _, err := parseEndpoint(cfg.Servers[0]); err != nil {
		return err
	}
	if cfg.TimeoutConnection <= 0 {
		return errors.New("LDAP_TIMEOUT_CONNECTION must be greater than zero")
	}
	if cfg.TimeoutLDAPOperation <= 0 {
		return errors.New("LDAP_TIMEOUT_LDAP_OPERATION must be greater than zero")
	}

	// tls
	if _, err := loadTLSConfig("", cfg.TLSCACert, cfg.TLSCert, cfg.TLSKey); err != nil {
		return err
	}

	// search+bind
	if cfg.SearchBindDN == "" {
		return errors.New("LDAP_SEARCH_BIND_DN is required")
	}
	if cfg.SearchBindPassword == "" {
		return errors.New("LDAP_SEARCH_BIND_PASSWORD is required")
	}
	if cfg.SearchBaseDN == "" {
		return errors.New("LDAP_SEARCH_BASE_DN is required")
	}
	if cfg.SearchAttribute == "" {
		return errors.New("LDAP_SEARCH_ATTRIBUTE is required")
	}
	if cfg.SearchDNAttribute == "" {
		return errors.New("LDAP_SEARCH_DN_ATTRIBUTE is required")
	}
	if _, err := ldap.CompileFilter(cfg.SearchFilter); err != nil {
		return fmt.Errorf("invalid LDAP_SEARCH_FILTER: %w", err)
	}
	if _, err := cfg.SearchScope.ldapScope(); err != nil {
		return err
	}

	return nil
}

func (cfg config) userFilter(username string) string {
	return fmt.Sprintf("(&%s(%s=%s))", cfg.SearchFilter, cfg.SearchAttribute, ldap.EscapeFilter(username))
}

func getEnvString(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	strValue := os.Getenv(key)
	if value, err := strconv.Atoi(strValue); err == nil {
		return value
	}
	return fallback
}

func getEnvBool(name string, fallback bool) bool {
	strValue := os.Getenv(name)
	if value, err := strconv.ParseBool(strValue); err == nil {
		return value
	}
	return fallback
}
