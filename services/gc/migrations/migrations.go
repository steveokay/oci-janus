// Package migrations bundles the gc service's SQL migrations into an
// embed.FS so the server can run them at startup via goose without
// depending on the filesystem of the deployed container image.
//
// FE-API-032 introduced the first GC-owned schema. Until this milestone
// services/gc was a stateless cron-driven worker; the new gc_runs table
// gives the dashboard durable visibility into sweep history without
// adding a sibling table to registry-metadata's schema.
package migrations

import "embed"

// FS is the embedded SQL migration set. goose.SetBaseFS(FS) is called
// from the server bootstrap so migrations apply before the gRPC server
// starts accepting traffic.
//
//go:embed *.sql
var FS embed.FS
