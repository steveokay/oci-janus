module github.com/steveokay/oci-janus/infra/scanner-plugins/engine-server

// Single-file HTTP wrapper using only the standard library. Pinned to the
// same toolchain as the rest of the monorepo so cross-builds in the engine
// sidecar image stay reproducible. Deliberately OUTSIDE go.work (GOWORK=off
// builds) like the adapter shims.
go 1.25.11
