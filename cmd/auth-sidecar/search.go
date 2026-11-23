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
	"slices"

	"github.com/go-ldap/ldap/v3"
)

var (
	errEmptyCredentials   = errors.New("username and password must not be empty")
	errUserNotFound       = errors.New("user not found")
	errMultipleUsersFound = errors.New("multiple users found")
	errNoGroupMapping     = errors.New("user has no mapped LDAP group")
)

type ldapDirectory interface {
	Bind(username, password string) error
	Search(request *ldap.SearchRequest) (*ldap.SearchResult, error)
}

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

	return searchDirectory(ctx, cfg, directory, username, password)
}

func searchDirectory(ctx context.Context, cfg config, directory ldapDirectory, username, password string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := directory.Bind(cfg.SearchBindDN, cfg.SearchBindPassword); err != nil {
		return "", err
	}

	userDN, err := searchUsers(cfg, directory, username, password)
	if err != nil {
		return "", err
	}

	if len(cfg.GroupMappings) == 0 {
		return username, nil
	}

	return searchGroups(cfg, directory, userDN)
}

func searchUsers(cfg config, directory ldapDirectory, username, password string) (string, error) {
	scope, err := cfg.UserSearchScope.ldapScope()
	if err != nil {
		return "", err
	}
	searchResult, err := directory.Search(ldap.NewSearchRequest(cfg.UserSearchBaseDN, scope,
		ldap.NeverDerefAliases, 2, int(cfg.TimeoutLDAPOperation.Seconds()), false,
		cfg.userFilter(username), []string{cfg.UserSearchDNAttribute}, nil))
	if err != nil {
		return "", err
	}
	if len(searchResult.Entries) == 0 {
		return "", errUserNotFound
	}
	if len(searchResult.Entries) != 1 {
		return "", errMultipleUsersFound
	}

	dnValues := searchResult.Entries[0].GetEqualFoldAttributeValues(cfg.UserSearchDNAttribute)
	if len(dnValues) != 1 || dnValues[0] == "" {
		return "", fmt.Errorf("user entry must contain exactly one nonempty %s value", cfg.UserSearchDNAttribute)
	}
	userDN := dnValues[0]
	if _, err = ldap.ParseDN(userDN); err != nil {
		return "", fmt.Errorf("invalid user DN in %s: %w", cfg.UserSearchDNAttribute, err)
	}
	if err = directory.Bind(userDN, password); err != nil {
		return "", err
	}

	return userDN, nil
}

func searchGroups(cfg config, directory ldapDirectory, userDN string) (string, error) {
	scope, err := cfg.GroupSearchScope.ldapScope()
	if err != nil {
		return "", err
	}
	searchResult, err := directory.Search(ldap.NewSearchRequest(cfg.GroupSearchBaseDN, scope,
		ldap.NeverDerefAliases, 0, int(cfg.TimeoutLDAPOperation.Seconds()), false,
		cfg.groupFilter(userDN), []string{cfg.GroupSearchDNAttribute}, nil))
	if err != nil {
		return "", fmt.Errorf("group search: %w", err)
	}

	groups := make([]*ldap.DN, 0, len(searchResult.Entries))
	for _, entry := range searchResult.Entries {
		values := entry.GetEqualFoldAttributeValues(cfg.GroupSearchDNAttribute)
		if len(values) != 1 || values[0] == "" {
			return "", fmt.Errorf("group entry must contain exactly one nonempty %s value", cfg.GroupSearchDNAttribute)
		}
		groupDN, err := ldap.ParseDN(values[0])
		if err != nil {
			return "", fmt.Errorf("invalid group DN in %s: %w", cfg.GroupSearchDNAttribute, err)
		}
		groups = append(groups, groupDN)
	}
	for _, mapping := range cfg.GroupMappings {
		mappedDN, err := ldap.ParseDN(mapping.GroupDN)
		if err != nil {
			return "", fmt.Errorf("invalid mapped group DN: %w", err)
		}
		if slices.ContainsFunc(groups, mappedDN.EqualFold) {
			return mapping.ACLUser, nil
		}
	}
	return "", errNoGroupMapping
}
