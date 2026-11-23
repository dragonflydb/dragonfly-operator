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
	"strconv"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/sethvargo/go-envconfig/v2"
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
	HTTPConfig `env:", prefix=HTTP_"`
	LDAPConfig `env:", prefix=LDAP_"`
}

type HTTPConfig struct {
	Port              string        `env:"PORT,default=8080"`
	ReadHeaderTimeout time.Duration `env:"READ_HEADER_TIMEOUT,default=5s"`
	ReadTimeout       time.Duration `env:"READ_TIMEOUT,default=10s"`
	WriteTimeout      time.Duration `env:"WRITE_TIMEOUT,default=10s"`
	IdleTimeout       time.Duration `env:"IDLE_TIMEOUT,default=10s"`
	ShutdownTimeout   time.Duration `env:"SHUTDOWN_TIMEOUT,default=10s"`
}

type LDAPConfig struct {
	// general
	Servers              []string      `env:"SERVERS"` // TODO: Implement support for multiple LDAP servers.
	TimeoutConnection    time.Duration `env:"TIMEOUT_CONNECTION,default=10s"`
	TimeoutLDAPOperation time.Duration `env:"TIMEOUT_LDAP_OPERATION,default=10s"`

	// tls
	UseStartTLS bool   `env:"USE_STARTTLS,default=false"`
	TLSCACert   string `env:"TLS_CA_CERT"`
	TLSCert     string `env:"TLS_CERT"`
	TLSKey      string `env:"TLS_KEY"`

	// search+bind
	SearchBindDN       string `env:"SEARCH_BIND_DN"`
	SearchBindPassword string `env:"SEARCH_BIND_PASSWORD"`

	// user search
	UserSearchBaseDN      string      `env:"USER_SEARCH_BASE_DN"`
	UserSearchFilter      string      `env:"USER_SEARCH_FILTER,default=(objectClass=*)"`
	UserSearchAttribute   string      `env:"USER_SEARCH_ATTRIBUTE,default=uid"`
	UserSearchDNAttribute string      `env:"USER_SEARCH_DN_ATTRIBUTE,default=entryDN"`
	UserSearchScope       searchScope `env:"USER_SEARCH_SCOPE,default=sub"`

	// group search
	GroupMappings          groupMappings `env:"GROUP_MAPPINGS"`
	GroupSearchBaseDN      string        `env:"GROUP_SEARCH_BASE_DN"`
	GroupSearchFilter      string        `env:"GROUP_SEARCH_FILTER,default=(objectClass=*)"`
	GroupSearchAttribute   string        `env:"GROUP_SEARCH_ATTRIBUTE,default=member"`
	GroupSearchDNAttribute string        `env:"GROUP_SEARCH_DN_ATTRIBUTE,default=entryDN"`
	GroupSearchScope       searchScope   `env:"GROUP_SEARCH_SCOPE,default=sub"`
}

type groupMapping struct {
	GroupDN string `json:"group_dn"`
	ACLUser string `json:"acl_user"`
}

type groupMappings []groupMapping

func (mappings *groupMappings) EnvDecode(_ context.Context, value string) error {
	return json.Unmarshal([]byte(value), mappings)
}

func loadConfig() (config, error) {
	var cfg config
	ctx := context.Background()
	if err := envconfig.Process(ctx, &cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.validate()
}

func (cfg config) validate() error {
	// http config
	if port, err := strconv.ParseUint(cfg.Port, 10, 16); err != nil || port == 0 {
		return errors.New("http port must be an integer between 1 and 65535")
	}
	if cfg.ReadHeaderTimeout <= 0 {
		return errors.New("http read header timeout must be greater than zero")
	}
	if cfg.ReadTimeout <= 0 {
		return errors.New("http read timeout must be greater than zero")
	}
	if cfg.WriteTimeout <= 0 {
		return errors.New("http write timeout must be greater than zero")
	}
	if cfg.IdleTimeout <= 0 {
		return errors.New("http idle timeout must be greater than zero")
	}
	if cfg.ShutdownTimeout <= 0 {
		return errors.New("http shutdown timeout must be greater than zero")
	}

	// ldap config

	// general
	if len(cfg.Servers) != 1 {
		return errors.New("ldap servers must contain exactly one endpoint")
	}
	if _, err := parseEndpoint(cfg.Servers[0]); err != nil {
		return err
	}
	if cfg.TimeoutConnection <= 0 {
		return errors.New("ldap timeout connection must be greater than zero")
	}
	if cfg.TimeoutLDAPOperation <= 0 {
		return errors.New("ldap timeout ldap operation must be greater than zero")
	}

	// tls
	if _, err := loadTLSConfig("", cfg.TLSCACert, cfg.TLSCert, cfg.TLSKey); err != nil {
		return err
	}

	// search+bind
	if cfg.SearchBindDN == "" {
		return errors.New("ldap search bind dn is required")
	}
	if cfg.SearchBindPassword == "" {
		return errors.New("ldap search bind password is required")
	}

	// user search
	if cfg.UserSearchBaseDN == "" {
		return errors.New("ldap search base dn is required")
	}
	if cfg.UserSearchAttribute == "" {
		return errors.New("ldap search attribute is required")
	}
	if cfg.UserSearchDNAttribute == "" {
		return errors.New("ldap search dn attribute is required")
	}
	if _, err := ldap.CompileFilter(cfg.UserSearchFilter); err != nil {
		return fmt.Errorf("invalid ldap search filter: %w", err)
	}
	if _, err := cfg.UserSearchScope.ldapScope(); err != nil {
		return err
	}

	// group search
	if len(cfg.GroupMappings) == 0 {
		return nil
	}
	if cfg.GroupSearchBaseDN == "" {
		return errors.New("ldap group search base dn is required")
	}
	if cfg.GroupSearchAttribute == "" {
		return errors.New("ldap group search attribute is required")
	}
	if cfg.GroupSearchDNAttribute == "" {
		return errors.New("ldap group search dn attribute is required")
	}
	if _, err := ldap.CompileFilter(cfg.GroupSearchFilter); err != nil {
		return fmt.Errorf("invalid ldap group search filter: %w", err)
	}
	if _, err := cfg.GroupSearchScope.ldapScope(); err != nil {
		return fmt.Errorf("invalid ldap group search scope: %w", err)
	}
	for i, mapping := range cfg.GroupMappings {
		if mapping.GroupDN == "" {
			return fmt.Errorf("ldap group mapping %d group dn is required", i)
		}
		if _, err := ldap.ParseDN(mapping.GroupDN); err != nil {
			return fmt.Errorf("invalid ldap group mapping %d group dn: %w", i, err)
		}
		if mapping.ACLUser == "" {
			return fmt.Errorf("ldap group mapping %d acl user is required", i)
		}
	}

	return nil
}

func (cfg config) userFilter(username string) string {
	return fmt.Sprintf("(&%s(%s=%s))", cfg.UserSearchFilter, cfg.UserSearchAttribute, ldap.EscapeFilter(username))
}

func (cfg config) groupFilter(userDN string) string {
	return fmt.Sprintf("(&%s(%s=%s))", cfg.GroupSearchFilter, cfg.GroupSearchAttribute, ldap.EscapeFilter(userDN))
}
