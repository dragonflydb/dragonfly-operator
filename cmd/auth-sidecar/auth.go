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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"

	"github.com/go-ldap/ldap/v3"
)

func connect(cfg config, ldapURL string) (*ldap.Conn, error) {
	endpoint, err := parseEndpoint(ldapURL)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := loadTLSConfig(endpoint.Hostname(), cfg.TLSCACert, cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		return nil, err
	}
	connection, err := ldap.DialURL(endpoint.String(),
		ldap.DialWithDialer(&net.Dialer{Timeout: cfg.TimeoutConnection}),
		ldap.DialWithTLSConfig(tlsConfig),
	)
	if err != nil {
		return nil, err
	}
	if cfg.UseStartTLS && endpoint.Scheme == "ldap" {
		if err = connection.StartTLS(tlsConfig); err != nil {
			connection.Close()
			return nil, err
		}
	}
	return connection, nil
}

func loadTLSConfig(serverName, caCert, cert, key string) (*tls.Config, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if caCert != "" {
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, err
		}
		if !roots.AppendCertsFromPEM([]byte(caCert)) {
			return nil, errors.New("error parsing CA certificate")
		}
		tlsConfig.RootCAs = roots
	}
	if cert != "" && key != "" {
		certificate, err := tls.X509KeyPair([]byte(cert), []byte(key))
		if err != nil {
			return nil, err
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}
	return tlsConfig, nil
}
