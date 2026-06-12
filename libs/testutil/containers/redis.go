//go:build integration

package containers

import (
	"context"
	"fmt"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Redis starts a Redis 7-alpine container and returns the addr string (host:port).
// The container is terminated when t finishes.
func Redis(t testing.TB) string {
	t.Helper()
	addr, cleanup, err := RedisNoT(context.Background())
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	t.Cleanup(cleanup)
	return addr
}

// RedisNoT starts a Redis container without a testing.TB.
// The caller must call the returned cleanup func when done.
func RedisNoT(ctx context.Context) (addr string, cleanup func(), err error) {
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:7-alpine",
			ExposedPorts: []string{"6379/tcp"},
			// PONG response confirms the server is accepting connections.
			WaitingFor: wait.ForLog("* Ready to accept connections"),
		},
		Started: true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("start redis: %w", err)
	}
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "6379")
	return fmt.Sprintf("%s:%s", host, port.Port()),
		func() { _ = c.Terminate(context.Background()) }, nil
}
