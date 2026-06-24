// Package eventconsumer subscribes the signer service to inbound RabbitMQ
// events that should trigger an automatic sign operation.
//
// At present this is only the FUT-017 cache.populated event published by
// services/proxy after every successful pull-through cache write. Each event
// is checked against the per-(tenant, upstream) policy stored in
// proxy_cache_sign_policies; when auto_sign=true and key_id is non-empty
// the consumer invokes the same signing primitive the gRPC SignManifest
// handler does, then idempotently no-ops if a signature for that key
// already exists.
//
// Errors are logged + swallowed (return nil) so a single bad event never
// blocks the consumer goroutine. The exception is queue-full / publisher
// downstream errors which propagate so the broker can NACK + redeliver
// after backoff — the same pattern services/scanner uses for the
// push.completed consumer.
package eventconsumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/signer/internal/repository"
	"github.com/steveokay/oci-janus/services/signer/internal/signing"
	"github.com/steveokay/oci-janus/services/signer/internal/sigstore"
)

// PolicyLookup is the narrow contract the cache-populated handler needs
// from the persistence layer. Returning (nil, nil) means "no policy row
// for this upstream" which is treated the same as auto_sign=false.
//
// Kept as an interface (rather than depending on *repository.Repository
// directly) so unit tests can plug in lightweight fakes without
// constructing a pgxpool.
type PolicyLookup interface {
	GetProxyCacheSignPolicy(ctx context.Context, tenantID, upstreamName string) (*repository.ProxyCacheSignPolicy, error)
}

// SignatureLookup is the read-only slice of sigstore.Store the consumer
// needs to enforce idempotency: skip the sign call when a record with
// the same (tenant_id, manifest_digest, signer_id=key_id) is already
// present in the in-memory cache or the DB.
//
// FindRec returns nil when no matching record exists.
type SignatureLookup interface {
	FindRec(ctx context.Context, tenantID, manifestDigest, signerID string) *sigstore.Record
}

// SignatureWriter is the side of sigstore.Store used to persist a freshly
// produced signature. Add takes the in-process write-through cache + best-
// effort DB upsert path; the consumer doesn't care which leg succeeds, the
// store's own logging covers the error case.
type SignatureWriter interface {
	Add(rec *sigstore.Record)
}

// Handler consumes cache.populated events and auto-signs the cached
// manifest when the (tenant_id, upstream_name) policy opts in.
//
// signer is the same signing.Signer the gRPC SignManifest handler uses —
// reuse keeps the produced signature bit-identical regardless of which
// surface triggered it.
type Handler struct {
	signer  signing.Signer
	store   *sigstore.Store
	lookup  PolicyLookup
	sigRead SignatureLookup
	sigAdd  SignatureWriter
}

// NewHandler builds the FUT-017 cache.populated consumer handler. Pass
// the live *sigstore.Store for sigRead/sigAdd — production code wires
// the same store reference into all three positions so the in-memory
// cache and DB upsert path stay consistent.
func NewHandler(s signing.Signer, store *sigstore.Store, lookup PolicyLookup) *Handler {
	return &Handler{
		signer:  s,
		store:   store,
		lookup:  lookup,
		sigRead: store,
		sigAdd:  store,
	}
}

// CachePopulatedConsumerConfig returns the consumer.Config for the
// FUT-017 cache.populated queue. The queue name is namespaced so it
// can't clash with the scanner's own subscription to the same routing
// key (each consumer owns its own durable queue per CLAUDE.md §6).
func CachePopulatedConsumerConfig() consumer.Config {
	return consumer.Config{
		Queue:      "signer.cache.populated",
		RoutingKey: events.RoutingCachePopulated,
		MaxRetries: 3,
	}
}

// Handle is the consumer.Handler entry point.
//
// Return-nil semantics: nearly every branch returns nil so the broker
// ACKs the message and we don't loop on a permanently-bad event.
// Persistent errors (policy lookup blew up, sign failed) are logged and
// swallowed — losing a single auto-sign is far cheaper than wedging the
// whole consumer queue. The sigstore.Store.Add path already handles DB
// flakes by warning + leaving the record in-memory, so a flaky DB does
// not require a NACK either.
func (h *Handler) Handle(ctx context.Context, evt events.Event) error {
	var payload events.CachePopulatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		// Unparseable payload — log and ACK so it doesn't loop on the queue.
		slog.ErrorContext(ctx, "cache.populated: unparseable payload — dropping",
			"event_id", evt.ID,
			"error", err,
		)
		return nil
	}

	// Defensive: a malformed payload missing the routing key fields can't
	// be acted on. Log + ACK rather than NACKing into the DLX — there is
	// no recovery path.
	if payload.TenantID == "" || payload.UpstreamName == "" || payload.ManifestDigest == "" {
		slog.WarnContext(ctx, "cache.populated: payload missing required fields — dropping",
			"event_id", evt.ID,
			"tenant_id", payload.TenantID,
			"upstream_name", payload.UpstreamName,
			"manifest_digest", payload.ManifestDigest,
		)
		return nil
	}

	policy, err := h.lookup.GetProxyCacheSignPolicy(ctx, payload.TenantID, payload.UpstreamName)
	if err != nil {
		// DB blip — log + skip. Auto-sign is opportunistic; a missed event
		// is recoverable via the manual SignManifest RPC.
		slog.ErrorContext(ctx, "cache.populated: policy lookup failed — skipping",
			"event_id", evt.ID,
			"tenant_id", payload.TenantID,
			"upstream_name", payload.UpstreamName,
			"error", err,
		)
		return nil
	}

	if !shouldAutoSign(policy) {
		// Most common branch — feature is off for this upstream. Nothing
		// to log at INFO; the high-frequency cache.populated firehose
		// would drown the journal.
		return nil
	}

	// Idempotency guard: if a signature for this (tenant, manifest) under
	// the same key already exists, don't re-sign. The signatures table
	// uniqueness is keyed on (tenant_id, manifest_digest, signer_id) and
	// FindRec looks up exactly that triple. Same-key re-signs would
	// succeed on the upsert but produce a redundant signed_at bump and
	// burn CPU + KMS quota for no behaviour change.
	if existing := h.sigRead.FindRec(ctx, payload.TenantID, payload.ManifestDigest, policy.KeyID); existing != nil {
		slog.DebugContext(ctx, "cache.populated: signature already exists — skipping re-sign",
			"event_id", evt.ID,
			"tenant_id", payload.TenantID,
			"manifest_digest", payload.ManifestDigest,
			"key_id", policy.KeyID,
		)
		return nil
	}

	// Repository name for the proxy-cache path is reconstructed from the
	// upstream name + image. Format mirrors the convention used elsewhere
	// in the cache subsystem (proxy/<upstream>/<image>) so downstream
	// verification can find the same identity.
	repoName := fmt.Sprintf("proxy/%s/%s", payload.UpstreamName, payload.Image)

	sigB64, err := h.signer.SignPayload(payload.TenantID, repoName, payload.ManifestDigest)
	if err != nil {
		slog.ErrorContext(ctx, "cache.populated: sign failed",
			"event_id", evt.ID,
			"tenant_id", payload.TenantID,
			"manifest_digest", payload.ManifestDigest,
			"key_id", policy.KeyID,
			"error", err,
		)
		return nil
	}

	sigDigest, err := signing.SignatureDigest(sigB64)
	if err != nil {
		slog.ErrorContext(ctx, "cache.populated: signature digest failed",
			"event_id", evt.ID,
			"manifest_digest", payload.ManifestDigest,
			"error", err,
		)
		return nil
	}

	// signer_id == key_id is the convention used by SignManifest when the
	// caller doesn't override it — keeps the idempotency check above
	// honest because that's also what FindRec searches on.
	rec := &sigstore.Record{
		TenantID:        payload.TenantID,
		SignerID:        policy.KeyID,
		ManifestDigest:  payload.ManifestDigest,
		RepositoryName:  repoName,
		SignatureDigest: sigDigest,
		KeyID:           policy.KeyID,
		SigB64:          sigB64,
		SignedAt:        time.Now(),
	}
	h.sigAdd.Add(rec)

	slog.InfoContext(ctx, "cache.populated: signed cached manifest",
		"tenant_id", payload.TenantID,
		"upstream_name", payload.UpstreamName,
		"manifest_digest", payload.ManifestDigest,
		"key_id", policy.KeyID,
	)
	return nil
}

// shouldAutoSign captures the two-knob gate so tests + the live handler
// agree on what "policy opts in" means. A nil policy (no row in the DB)
// is equivalent to auto_sign=false; an empty key_id is treated as
// disabled regardless of the flag so flipping auto_sign on without
// picking a key never produces signatures with an empty signer id.
func shouldAutoSign(p *repository.ProxyCacheSignPolicy) bool {
	if p == nil {
		return false
	}
	if !p.AutoSign {
		return false
	}
	if p.KeyID == "" {
		return false
	}
	return true
}
