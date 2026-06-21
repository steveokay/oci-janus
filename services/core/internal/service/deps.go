package service

import (
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
)

// EventPublisher is the exported alias for the eventPublisher interface so that
// packages outside this service (e.g. handler tests) can implement a no-op
// publisher without importing the concrete *publisher.Publisher type.
type EventPublisher = eventPublisher

// newRegistryWithClients constructs a Registry from pre-built gRPC clients and
// an eventPublisher. Used by tests in this package that wire up in-process fake
// gRPC servers (via bufconn) without going through NewRegistry. The sampler
// defaults to "always publish" so the existing push.completed flow is
// unaffected; FE-API-042 pull-publish tests override it explicitly.
func newRegistryWithClients(
	meta metadatav1.MetadataServiceClient,
	storage storagev1.StorageServiceClient,
	uploads *UploadStore,
	referrers *ReferrerStore,
	pub eventPublisher,
) *Registry {
	return &Registry{
		metadata:   meta,
		storage:    storage,
		uploads:    uploads,
		referrers:  referrers,
		publisher:  pub,
		pullSample: func() bool { return true },
	}
}

// NewRegistryWithClients is the exported variant of newRegistryWithClients for
// use by cross-package tests (e.g. handler integration tests) that need to
// inject fake gRPC clients and a no-op publisher without a real AMQP broker.
func NewRegistryWithClients(
	meta metadatav1.MetadataServiceClient,
	storage storagev1.StorageServiceClient,
	uploads *UploadStore,
	referrers *ReferrerStore,
	pub EventPublisher,
) *Registry {
	return newRegistryWithClients(meta, storage, uploads, referrers, pub)
}
