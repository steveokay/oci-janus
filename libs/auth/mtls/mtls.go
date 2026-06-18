// Package mtls provides helpers for constructing mTLS tls.Config values used
// by all internal gRPC clients and servers. Every service-to-service call in the
// registry requires mutual certificate verification — no unauthenticated gRPC.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
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
		MinVersion:   tls.VersionTLS13,
	}, nil
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
		MinVersion:   tls.VersionTLS13,
	}, nil
}
