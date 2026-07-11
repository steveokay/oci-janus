package handler

import (
	"net/http"

	"github.com/steveokay/oci-janus/libs/auth/bearer"
)

// scimVerifier verifies a raw SCIM bearer token. Returns (true, nil) only when
// the config is enabled and the token matches.
type scimVerifier func(raw string) (bool, error)

// requireSCIMAuth gates a SCIM handler on the global SCIM token. It is NOT the
// user auth path: the SCIM principal carries no RBAC roles and is valid only on
// /scim/v2/*. Fail-closed — any error or mismatch is a 401. It never returns an
// oracle distinguishing "no token", "wrong token", or "feature disabled".
func requireSCIMAuth(verify scimVerifier, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, ok := bearer.Extract(r.Header.Get("Authorization"))
		if !ok || raw == "" {
			writeSCIMError(w, http.StatusUnauthorized, "", "missing or malformed bearer token")
			return
		}
		valid, err := verify(raw)
		if err != nil || !valid {
			writeSCIMError(w, http.StatusUnauthorized, "", "invalid SCIM token")
			return
		}
		next(w, r)
	}
}
