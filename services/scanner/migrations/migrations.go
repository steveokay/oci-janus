// Package migrations bundles the scanner service's SQL migrations into an
// embed.FS so the server can run them at startup via goose without depending
// on the filesystem of the deployed container image.
//
// Scanner is the eighth service to own its own database (joining auth,
// metadata, tenant, proxy, webhook, audit, and signer). Until FE-API-018
// the scanner held no durable state of its own — scan results were written
// to registry-metadata. The tables added here back scan policies
// (FE-API-018) and compliance reports (FE-API-019) since both are
// per-tenant operational configuration owned by the scanner service.
package migrations

import "embed"

// FS is the embedded SQL migration set. goose.SetBaseFS(FS) is called from
// the server bootstrap.
//
//go:embed *.sql
var FS embed.FS
