//go:build integration

package containers

import (
	"context"
	"fmt"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// MinIOConfig holds the connection details for a MinIO test instance.
type MinIOConfig struct {
	Endpoint  string // host:port
	AccessKey string
	SecretKey string
	Bucket    string
}

// MinIO starts a MinIO container and returns connection details.
// The container is terminated when t finishes.
func MinIO(t testing.TB) MinIOConfig {
	t.Helper()
	cfg, cleanup, err := MinIONoT(context.Background())
	if err != nil {
		t.Fatalf("start minio: %v", err)
	}
	t.Cleanup(cleanup)
	return cfg
}

// MinIONoT starts a MinIO container without a testing.TB.
// The caller must call the returned cleanup func when done.
func MinIONoT(ctx context.Context) (cfg MinIOConfig, cleanup func(), err error) {
	const (
		accessKey = "minioadmin"
		secretKey = "minioadmin"
		bucket    = "test-registry"
	)
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "minio/minio:latest",
			ExposedPorts: []string{"9000/tcp"},
			Env: map[string]string{
				"MINIO_ROOT_USER":     accessKey,
				"MINIO_ROOT_PASSWORD": secretKey,
			},
			// MinIO server needs the data directory and API mode flag.
			Cmd: []string{"server", "/data"},
			// MinIO prints this line when the API endpoint is ready.
			WaitingFor: wait.ForLog("API:"),
		},
		Started: true,
	})
	if err != nil {
		return MinIOConfig{}, nil, fmt.Errorf("start minio: %w", err)
	}
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "9000")
	endpoint := fmt.Sprintf("%s:%s", host, port.Port())
	return MinIOConfig{
			Endpoint:  endpoint,
			AccessKey: accessKey,
			SecretKey: secretKey,
			Bucket:    bucket,
		},
		func() { _ = c.Terminate(context.Background()) }, nil
}
