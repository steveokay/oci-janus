// healthcheck is a minimal HTTP health probe binary included in distroless service
// images. It exits 0 if GET /healthz returns 200, non-zero otherwise.
// Usage: /healthcheck [http://addr:port/healthz]
package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	addr := "http://localhost:8080/healthz"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	resp, err := http.Get(addr) //nolint:noctx,gosec
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
