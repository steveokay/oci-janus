// Package mtls provides helpers for constructing mTLS tls.Config values used
// by all internal gRPC clients and servers. Every service-to-service call in the
// registry requires mutual certificate verification — no unauthenticated gRPC.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// ServerTLSConfig returns a tls.Config for gRPC servers that require and verify client certs.
func ServerTLSConfig(caCertPath, certPath, keyPath string) (*tls.Config, error) {
	caCert, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA cert")
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load server cert/key: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		// PENTEST-012: TLS 1.3 minimum for all internal mTLS. TLS 1.3 mandates
		// forward secrecy + AEAD-only cipher suites and removes legacy
		// renegotiation. There are no external clients on these gRPC ports
		// (all calls are service-to-service inside the cluster), so backwards
		// compatibility with TLS 1.2-only clients is a non-issue.
		MinVersion: tls.VersionTLS13,
	}, nil
}

// ClientCreds returns gRPC TransportCredentials for outbound dials.
//
// REDESIGN-001 Phase 3.4 rule-of-three extraction. Previously duplicated as
// `buildClientCreds` in services/auth and services/metadata; lifted here so
// the remaining services don't copy-paste.
//
// When all three cert paths are configured, builds the standard mTLS
// credentials via ClientTLSConfig + credentials.NewTLS. When any path is
// empty (typical dev compose stack without certs), returns insecure
// credentials — production-mode startup must reject this via
// libs/config/loader.ValidateMTLSConfig, which is the layer that
// distinguishes "dev fallback" from "missing config in prod."
//
// serverName must match the expected SAN/CN on the remote server's cert
// (e.g. "registry-tenant"). In dev (insecure) mode it's ignored.
func ClientCreds(caCertPath, certPath, keyPath, serverName string) (credentials.TransportCredentials, error) {
	if caCertPath == "" || certPath == "" || keyPath == "" {
		return insecure.NewCredentials(), nil
	}
	tlsCfg, err := ClientTLSConfig(caCertPath, certPath, keyPath, serverName)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(tlsCfg), nil
}

// ClientTLSConfig returns a tls.Config for gRPC clients presenting a cert.
func ClientTLSConfig(caCertPath, certPath, keyPath, serverName string) (*tls.Config, error) {
	caCert, err := os.ReadFile(caCertPath)
	if err != nil {
		return nil, fmt.Errorf("read CA cert: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA cert")
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load client cert/key: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
		// PENTEST-012: TLS 1.3 minimum for all internal mTLS. TLS 1.3 mandates
		// forward secrecy + AEAD-only cipher suites and removes legacy
		// renegotiation. There are no external clients on these gRPC ports
		// (all calls are service-to-service inside the cluster), so backwards
		// compatibility with TLS 1.2-only clients is a non-issue.
		MinVersion: tls.VersionTLS13,
	}, nil
}
