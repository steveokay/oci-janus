module github.com/steveokay/oci-janus/infra/scanner-plugins/grype-adapter

// Single-file binary using only the standard library. Pinned to the same
// toolchain as the rest of the monorepo so cross-builds in the scanner
// image stay reproducible.
go 1.25.11
