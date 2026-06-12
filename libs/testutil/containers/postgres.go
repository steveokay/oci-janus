//go:build integration

// Package containers provides testcontainers helpers for integration tests.
// Each function starts a container, wires up cleanup, and returns the address
// needed to connect. NoT variants accept a context.Context for use in TestMain
// where no *testing.T is available.
package containers

import (
	"context"
	"fmt"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Postgres starts a PostgreSQL 16-alpine container and returns a sslmode=disable DSN.
// The container is terminated when t finishes.
func Postgres(t testing.TB) string {
	t.Helper()
	dsn, cleanup, err := PostgresNoT(context.Background())
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(cleanup)
	return dsn
}

// PostgresNoT starts a PostgreSQL container without a testing.TB.
// The caller must call the returned cleanup func when done.
func PostgresNoT(ctx context.Context) (dsn string, cleanup func(), err error) {
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_USER":     "test",
				"POSTGRES_PASSWORD": "test",
				"POSTGRES_DB":       "testdb",
			},
			// Wait for two occurrences: once when initdb starts, once when the
			// server accepts connections after startup.
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		},
		Started: true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("start postgres: %w", err)
	}
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432")
	return fmt.Sprintf("postgres://test:test@%s:%s/testdb?sslmode=disable", host, port.Port()),
		func() { _ = c.Terminate(context.Background()) }, nil
}
