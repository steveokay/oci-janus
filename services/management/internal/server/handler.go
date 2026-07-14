package server

import (
	"net/http"

	httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

// buildHandler composes the HTTP middleware chain wrapped around the route
// mux. Extracted from Run so the chain is unit-testable (see handler_test.go).
//
// Order (outermost first): every response — including CORS preflight 204s and
// auth 401s / 404s that short-circuit before the mux — must carry the
// CLAUDE.md §17 security headers, so the shared httpmiddleware.SecureHeaders is
// the outermost wrapper (its own doc requires this). Management previously set
// nosniff + X-Frame-Options inline in the CORS middleware; consolidating onto
// the shared helper de-duplicates that, adds the missing X-XSS-Protection: 0,
// and matches every other service's server.go. CORS + RequestID run inside it.
func buildHandler(mux http.Handler, corsOrigin string) http.Handler {
	return httpmiddleware.SecureHeaders(middleware.CORS(corsOrigin)(middleware.RequestID(mux)))
}
