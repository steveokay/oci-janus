//go:build integration

package containers

import (
	"context"
	"fmt"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// RabbitMQ starts a RabbitMQ 3.13-management container and returns the AMQP URL.
// The container is terminated when t finishes.
func RabbitMQ(t testing.TB) string {
	t.Helper()
	url, cleanup, err := RabbitMQNoT(context.Background())
	if err != nil {
		t.Fatalf("start rabbitmq: %v", err)
	}
	t.Cleanup(cleanup)
	return url
}

// RabbitMQNoT starts a RabbitMQ container without a testing.TB.
// The caller must call the returned cleanup func when done.
func RabbitMQNoT(ctx context.Context) (amqpURL string, cleanup func(), err error) {
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "rabbitmq:3.13-management",
			ExposedPorts: []string{"5672/tcp"},
			Env: map[string]string{
				"RABBITMQ_DEFAULT_USER": "test",
				"RABBITMQ_DEFAULT_PASS": "test",
			},
			// Wait until the broker is fully booted and accepting connections.
			WaitingFor: wait.ForLog("Server startup complete"),
		},
		Started: true,
	})
	if err != nil {
		return "", nil, fmt.Errorf("start rabbitmq: %w", err)
	}
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5672")
	return fmt.Sprintf("amqp://test:test@%s:%s/", host, port.Port()),
		func() { _ = c.Terminate(context.Background()) }, nil
}
