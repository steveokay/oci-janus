package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// scimError is the RFC 7644 §3.12 error response shape.
type scimError struct {
	Schemas  []string `json:"schemas"`
	Status   string   `json:"status"`
	SCIMType string   `json:"scimType,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// writeSCIMError writes a SCIM-shaped error with the given HTTP status. Per
// RFC 7644 the "status" field is the numeric HTTP code as a string (e.g. "401").
func writeSCIMError(w http.ResponseWriter, status int, scimType, detail string) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(scimError{
		Schemas:  []string{"urn:ietf:params:scim:api:messages:2.0:Error"},
		Status:   strconv.Itoa(status),
		SCIMType: scimType,
		Detail:   detail,
	})
}

// scimUserSchemaURN is the RFC 7643 core User schema URN, carried in every
// User resource + on inbound create/replace bodies.
const scimUserSchemaURN = "urn:ietf:params:scim:schemas:core:2.0:User"

// scimUser is the SCIM core:2.0:User wire shape (the subset we support).
type scimUser struct {
	Schemas     []string    `json:"schemas"`
	ID          string      `json:"id,omitempty"`
	ExternalID  string      `json:"externalId,omitempty"`
	UserName    string      `json:"userName"`
	Name        *scimName   `json:"name,omitempty"`
	DisplayName string      `json:"displayName,omitempty"`
	Emails      []scimEmail `json:"emails,omitempty"`
	Active      bool        `json:"active"`
	Meta        *scimMeta   `json:"meta,omitempty"`
}

type scimName struct {
	Formatted string `json:"formatted,omitempty"`
}

type scimEmail struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary,omitempty"`
}

type scimMeta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created,omitempty"`
	LastModified string `json:"lastModified,omitempty"`
	Location     string `json:"location,omitempty"`
}

// scimListResponse is the RFC 7644 §3.4.2 paged list envelope.
type scimListResponse struct {
	Schemas      []string   `json:"schemas"`
	TotalResults int        `json:"totalResults"`
	StartIndex   int        `json:"startIndex"`
	ItemsPerPage int        `json:"itemsPerPage"`
	Resources    []scimUser `json:"Resources"`
}

// scimPatchRequest is the RFC 7644 §3.5.2 PATCH body. Only the `active` replace
// op is honoured in v1 (the deprovision path Okta/Entra send); other ops return
// 501.
type scimPatchRequest struct {
	Schemas    []string      `json:"schemas"`
	Operations []scimPatchOp `json:"Operations"`
}

type scimPatchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// primaryEmail returns the primary (or first) email value, or "".
func (u scimUser) primaryEmail() string {
	for _, e := range u.Emails {
		if e.Primary {
			return e.Value
		}
	}
	if len(u.Emails) > 0 {
		return u.Emails[0].Value
	}
	return ""
}

// toSCIMUser maps a repository.User to its SCIM wire representation. extID is the
// user's external_id (correlation key) — the repository.User struct does not
// carry it, so the caller threads it in.
func toSCIMUser(u *repository.User, extID string) scimUser {
	display := ""
	if u.DisplayName != nil {
		display = *u.DisplayName
	}
	su := scimUser{
		Schemas:     []string{scimUserSchemaURN},
		ID:          u.ID.String(),
		ExternalID:  extID,
		UserName:    u.Username,
		DisplayName: display,
		Active:      u.IsActive,
		Meta: &scimMeta{
			ResourceType: "User",
			Created:      u.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			LastModified: u.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			Location:     "/scim/v2/Users/" + u.ID.String(),
		},
	}
	if u.Email != "" {
		su.Emails = []scimEmail{{Value: u.Email, Primary: true}}
	}
	return su
}

var (
	reEqStr  = regexp.MustCompile(`^(userName|externalId)\s+eq\s+"([^"]*)"$`)
	reActive = regexp.MustCompile(`^active\s+eq\s+(true|false)$`)
)

// parseUserFilter supports exactly `userName eq "x"`, `externalId eq "y"`, and
// `active eq true|false` (spec D6). Empty filter matches all. Anything else is
// an error the handler maps to 400 scimType=invalidFilter.
func parseUserFilter(f string) (byUsername, byExternalID string, active *bool, err error) {
	f = strings.TrimSpace(f)
	if f == "" {
		return "", "", nil, nil
	}
	if m := reEqStr.FindStringSubmatch(f); m != nil {
		if m[1] == "userName" {
			return m[2], "", nil, nil
		}
		return "", m[2], nil, nil
	}
	if m := reActive.FindStringSubmatch(f); m != nil {
		b := m[1] == "true"
		return "", "", &b, nil
	}
	return "", "", nil, fmt.Errorf("unsupported filter: %q", f)
}
