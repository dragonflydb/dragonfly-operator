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
	"errors"
	"fmt"

	"github.com/go-ldap/ldap/v3"
)

var (
	errEmptyCredentials   = errors.New("username and password must not be empty")
	errUserNotFound       = errors.New("user not found")
	errMultipleUsersFound = errors.New("multiple users found")
)

func search(ctx context.Context, cfg config, username, password string) (string, error) {
	if username == "" || password == "" {
		return "", errEmptyCredentials
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	directory, err := connect(cfg, cfg.Servers[0])
	if err != nil {
		return "", fmt.Errorf("%s: %w", cfg.Servers[0], err)
	}
	defer directory.Close()

	if err = directory.Bind(cfg.SearchBindDN, cfg.SearchBindPassword); err != nil {
		return "", err
	}

	scope, err := cfg.SearchScope.ldapScope()
	if err != nil {
		return "", err
	}
	searchResult, err := directory.Search(ldap.NewSearchRequest(cfg.SearchBaseDN, scope,
		ldap.NeverDerefAliases, 2, cfg.TimeoutLDAPOperation, false,
		cfg.userFilter(username), []string{cfg.SearchDNAttribute}, nil))
	if err != nil {
		return "", err
	}
	if len(searchResult.Entries) == 0 {
		return "", errUserNotFound
	}
	if len(searchResult.Entries) != 1 {
		return "", errMultipleUsersFound
	}

	dnValues := searchResult.Entries[0].GetEqualFoldAttributeValues(cfg.SearchDNAttribute)
	if len(dnValues) != 1 || dnValues[0] == "" {
		return "", fmt.Errorf("user entry must contain exactly one nonempty %s value", cfg.SearchDNAttribute)
	}
	userDN := dnValues[0]
	if _, err = ldap.ParseDN(userDN); err != nil {
		return "", fmt.Errorf("invalid user DN in %s: %w", cfg.SearchDNAttribute, err)
	}
	if err = directory.Bind(userDN, password); err != nil {
		return "", err
	}
	return username, nil
}
