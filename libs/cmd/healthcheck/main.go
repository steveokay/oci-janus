// healthcheck is a minimal HTTP health probe binary included in distroless service
// images. It exits 0 if GET /healthz returns 200, non-zero otherwise.
// Usage: /healthcheck [http://addr:port/healthz]
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := "http://localhost:8080/healthz"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	// Use an explicit client with a timeout instead of http.DefaultClient, which
	// has no timeout. A hung healthcheck would block the container orchestrator's
	// liveness probe indefinitely, preventing pod replacement.
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(addr) //nolint:noctx
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		os.Exit(1)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}
}
