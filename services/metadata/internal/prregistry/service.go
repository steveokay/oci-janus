package prregistry

// service.go — the PR lifecycle dispatch (FUT-023 §7.4 + §7.5).
//
// HandleEvent is the single entry point the handler calls. It authenticates
// the webhook, parses it, derives the namespace name, and dispatches on the
// GitHub action to the create / teardown / promote-and-teardown branches. It
// returns a package-local Outcome (NOT a proto enum — the handler maps it) so
// this package stays proto-free and unit-testable.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// Outcome is the package-local result of dispatching one webhook. The handler
// maps each value onto the proto HandlePREventResponse_OUTCOME_* enum + an
// HTTP status; keeping it a local type keeps prregistry free of proto imports.
type Outcome string

const (
	// OutcomeIgnored — the event was well-formed but not actionable (ping,
	// non-pull_request event, an action we don't handle, a non-github
	// provider, or a payload we couldn't derive a namespace name from).
	OutcomeIgnored Outcome = "IGNORED"
	// OutcomeProvisioned — an opened/reopened PR created (or re-activated) its
	// namespace org.
	OutcomeProvisioned Outcome = "PROVISIONED"
	// OutcomePromotedAndTornDown — a merged PR promoted its artifacts to the
	// durable target org, then tore the namespace down.
	OutcomePromotedAndTornDown Outcome = "PROMOTED_AND_TORN_DOWN"
	// OutcomeTornDown — a closed (unmerged, or merged with no promote target)
	// PR tore its namespace down.
	OutcomeTornDown Outcome = "TORN_DOWN"
	// OutcomeDisabled — the integration is off/misconfigured (fail-closed);
	// the handler maps this to HTTP 404.
	OutcomeDisabled Outcome = "DISABLED"
)

const (
	// providerGitHub is the only SCM provider Phase 1 supports.
	providerGitHub = "github"

	// eventPullRequest is the X-GitHub-Event value we act on. "ping" and every
	// other event value are Ignored.
	eventPullRequest = "pull_request"

	// tagPageSize bounds each ListTags page during promote-on-merge; we loop
	// on the returned cursor until a short page signals the end.
	tagPageSize = 100

	// publishTimeout caps the best-effort event publish so it can't outlive
	// the request context by much.
	publishTimeout = 5 * time.Second

	// eventVersion stamps the event envelope (matches the "1.0" other
	// publishers use).
	eventVersion = "1.0"
)

// HandleEvent authenticates, parses, and dispatches a single SCM webhook.
//
// provider selects the SCM (Phase 1: "github" only — anything else ⇒
// Ignored). event is the raw webhook event name (X-GitHub-Event); signature is
// the raw X-Hub-Signature-256 header. rawBody must be the exact signed bytes.
//
// Returns the Outcome, the derived org name (empty when none was derived), and
// an error. The only non-nil errors are ErrSignatureMismatch (handler → 401)
// and a promote/store failure that must NOT be swallowed (handler → 5xx, so
// GitHub retries and the namespace survives). Disabled integrations return
// (OutcomeDisabled, "", nil) — that's a normal state, not an error.
func (s *Service) HandleEvent(ctx context.Context, cfg repository.PRRegistryConfig, provider string, rawBody []byte, signature, event string) (Outcome, string, error) {
	// 1. Provider gate — Phase 1 is GitHub-only.
	if provider != providerGitHub {
		return OutcomeIgnored, "", nil
	}

	// 2. Event gate — only `pull_request`. "ping" and everything else are
	//    healthy no-ops.
	if event != eventPullRequest {
		return OutcomeIgnored, "", nil
	}

	// 3. Authenticate BEFORE parsing — never touch an unverified body.
	if err := s.Verify(cfg, rawBody, signature); err != nil {
		if errors.Is(err, ErrFeatureDisabled) {
			return OutcomeDisabled, "", nil
		}
		// ErrSignatureMismatch (or any other verify failure) → handler 401.
		return OutcomeIgnored, "", err
	}

	// 4. Parse the (now-authenticated) body + derive the namespace name.
	pr, err := parseGitHubPR(rawBody)
	if err != nil {
		// A signed-but-malformed body is the sender's problem — log + ignore.
		slog.WarnContext(ctx, "prregistry: malformed pull_request payload", "error", err)
		return OutcomeIgnored, "", nil
	}
	orgName, err := deriveOrgName(pr.Repository.Name, pr.Number)
	if err != nil {
		slog.WarnContext(ctx, "prregistry: could not derive org name",
			"repo", pr.Repository.Name, "pr_number", pr.Number, "error", err)
		return OutcomeIgnored, "", nil
	}

	tenantID := cfg.TenantID
	sourceRepo := pr.Repository.FullName

	// 5. Dispatch on the PR action.
	switch pr.Action {
	case "opened", "reopened":
		return s.provision(ctx, tenantID, sourceRepo, orgName, pr.Number)

	case "closed":
		if pr.PullRequest.Merged && cfg.PromoteTargetOrg != "" {
			return s.promoteAndTearDown(ctx, cfg, tenantID, sourceRepo, orgName, pr.Number)
		}
		// Unmerged close, OR merged with no promote target ⇒ plain teardown.
		return s.tearDown(ctx, tenantID, sourceRepo, orgName, pr.Number, false, "")

	default:
		// synchronize, edited, labeled, ... — not actionable.
		return OutcomeIgnored, orgName, nil
	}
}

// provision handles opened/reopened: idempotently upsert the ephemeral org +
// its lifecycle row, then publish pr.namespace.provisioned. Re-delivery is
// safe — both writes are upserts.
func (s *Service) provision(ctx context.Context, tenantID uuid.UUID, sourceRepo, orgName string, prNumber int) (Outcome, string, error) {
	// SEC-085 adoption guard: only create a brand-new org, or reuse the exact
	// one our own active lifecycle row already points at (a GitHub re-delivery).
	// If an org with the derived pr-<repo>-<N> name already exists but isn't
	// ours, refuse — adopting it would let a later teardown cascade-delete an
	// operator-owned org this feature never minted. A refusal is a logged no-op
	// (OutcomeIgnored, no error) so it neither touches the foreign org nor
	// triggers a GitHub redelivery storm.
	existingOrgID, err := s.store.LookupOrgIDByName(ctx, tenantID.String(), orgName)
	switch {
	case errors.Is(err, repository.ErrNotFound):
		// No org by this name — safe to create below.
	case err != nil:
		return OutcomeIgnored, orgName, fmt.Errorf("lookup org %q: %w", orgName, err)
	default:
		ns, nsErr := s.store.GetPRNamespace(ctx, tenantID, providerGitHub, sourceRepo, prNumber)
		ours := nsErr == nil && ns != nil && ns.OrgID != nil && ns.OrgID.String() == existingOrgID
		if !ours {
			slog.WarnContext(ctx, "pr-registry: refusing to adopt pre-existing org (SEC-085 name collision)",
				"org_name", orgName, "source_repo", sourceRepo, "pr_number", prNumber)
			return OutcomeIgnored, orgName, nil
		}
	}

	orgIDStr, err := s.store.GetOrCreateOrganization(ctx, tenantID.String(), orgName)
	if err != nil {
		return OutcomeIgnored, orgName, fmt.Errorf("get or create org %q: %w", orgName, err)
	}
	orgID, err := uuid.Parse(orgIDStr)
	if err != nil {
		return OutcomeIgnored, orgName, fmt.Errorf("parse org id %q: %w", orgIDStr, err)
	}

	if _, err := s.store.UpsertPRNamespace(ctx, repository.PRNamespace{
		TenantID:   tenantID,
		OrgID:      &orgID,
		Provider:   providerGitHub,
		SourceRepo: sourceRepo,
		PRNumber:   prNumber,
		OrgName:    orgName,
	}); err != nil {
		return OutcomeIgnored, orgName, fmt.Errorf("upsert pr namespace: %w", err)
	}

	s.publish(ctx, tenantID, events.RoutingPRNamespaceProvisioned, events.PRNamespaceProvisionedPayload{
		TenantID:   tenantID.String(),
		Provider:   providerGitHub,
		SourceRepo: sourceRepo,
		PRNumber:   prNumber,
		OrgName:    orgName,
	})
	return OutcomeProvisioned, orgName, nil
}

// tearDown resolves the namespace row and (when still active) tears it down +
// publishes pr.namespace.torn_down. Idempotent: a missing or already-torn-down
// namespace is a no-op that still returns OutcomeTornDown so a GitHub
// re-delivery of the same close event is absorbed cleanly.
//
// promoted/targetOrg are stamped into the emitted event so the audit feed can
// tell "PR abandoned, GC'd" (promoted=false) from "PR merged, images promoted"
// (promoted=true).
func (s *Service) tearDown(ctx context.Context, tenantID uuid.UUID, sourceRepo, orgName string, prNumber int, promoted bool, targetOrg string) (Outcome, string, error) {
	ns, err := s.store.GetPRNamespace(ctx, tenantID, providerGitHub, sourceRepo, prNumber)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Never provisioned (or a duplicate close after teardown that also
			// removed the row) — nothing to do.
			return OutcomeTornDown, orgName, nil
		}
		return OutcomeIgnored, orgName, fmt.Errorf("get pr namespace: %w", err)
	}
	// Already torn down — idempotent no-op (don't re-publish).
	if ns.Status == "torn_down" {
		return OutcomeTornDown, orgName, nil
	}

	// TearDownPRNamespace tolerates a nil org (uuid.Nil) — pass through
	// whatever the row holds.
	orgID := uuid.Nil
	if ns.OrgID != nil {
		orgID = *ns.OrgID
	}
	if err := s.store.TearDownPRNamespace(ctx, tenantID, ns.ID, orgID); err != nil {
		return OutcomeIgnored, orgName, fmt.Errorf("tear down pr namespace: %w", err)
	}

	s.publish(ctx, tenantID, events.RoutingPRNamespaceTornDown, events.PRNamespaceTornDownPayload{
		TenantID:   tenantID.String(),
		Provider:   providerGitHub,
		SourceRepo: sourceRepo,
		PRNumber:   prNumber,
		OrgName:    orgName,
		Promoted:   promoted,
		TargetOrg:  targetOrg,
	})
	return OutcomeTornDown, orgName, nil
}

// promoteAndTearDown handles a merged PR with a promote target: it first
// promotes every (repo, tag) in the namespace into cfg.PromoteTargetOrg, THEN
// tears the namespace down. Promote runs fully before teardown so a
// non-immutable promote failure aborts (return the error) and leaves the
// namespace intact for a GitHub retry — we never delete artifacts we failed to
// copy. Returns OutcomePromotedAndTornDown on success.
func (s *Service) promoteAndTearDown(ctx context.Context, cfg repository.PRRegistryConfig, tenantID uuid.UUID, sourceRepo, orgName string, prNumber int) (Outcome, string, error) {
	// Resolve the namespace up front to get its org_id for the promote scan.
	// A missing namespace means nothing was ever provisioned — degrade to a
	// plain (idempotent) teardown rather than promoting from an empty org.
	ns, err := s.store.GetPRNamespace(ctx, tenantID, providerGitHub, sourceRepo, prNumber)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return OutcomeTornDown, orgName, nil
		}
		return OutcomeIgnored, orgName, fmt.Errorf("get pr namespace: %w", err)
	}
	if ns.Status == "torn_down" || ns.OrgID == nil {
		// Already torn down (duplicate merge delivery) — no-op.
		return OutcomeTornDown, orgName, nil
	}

	if err := s.promoteNamespace(ctx, cfg, tenantID, ns.OrgID.String(), orgName, prNumber); err != nil {
		// A non-immutable promote error aborts BEFORE teardown so the
		// namespace survives for retry. (Immutable-dest tags are skipped
		// inside promoteNamespace, never surfaced here.)
		return OutcomeIgnored, orgName, err
	}

	// Promote fully succeeded — now tear down + publish with promoted=true.
	// tearDown reports OutcomeTornDown; remap to PromotedAndTornDown so the
	// handler renders the merge-promote outcome. Any teardown error is
	// surfaced as-is.
	if _, _, err := s.tearDown(ctx, tenantID, sourceRepo, orgName, prNumber, true, cfg.PromoteTargetOrg); err != nil {
		return OutcomeIgnored, orgName, err
	}
	return OutcomePromotedAndTornDown, orgName, nil
}

// promoteNamespace copies every tag of every repository in the namespace org
// into cfg.PromoteTargetOrg (same repo + tag names). An immutable destination
// tag (repository.ErrImmutableTag) is logged and SKIPPED — one protected tag
// must not block the rest of the merge promotion (FUT-023 §7.5). Any OTHER
// promote error is returned so the caller aborts before teardown.
func (s *Service) promoteNamespace(ctx context.Context, cfg repository.PRRegistryConfig, tenantID uuid.UUID, orgID, srcOrg string, prNumber int) error {
	repos, err := s.store.ListRepositories(ctx, tenantID.String(), orgID, "")
	if err != nil {
		return fmt.Errorf("list repositories for org %s: %w", orgID, err)
	}

	note := fmt.Sprintf("PR #%d merge", prNumber)
	for _, repo := range repos {
		repoName := repo.GetName()
		last := ""
		for {
			tags, err := s.store.ListTags(ctx, tenantID.String(), repo.GetRepoId(), tagPageSize, last)
			if err != nil {
				return fmt.Errorf("list tags for repo %s: %w", repoName, err)
			}
			for _, tag := range tags {
				tagName := tag.GetName()
				_, err := s.store.PromoteTag(ctx, repository.PromoteTagInput{
					TenantID:        tenantID,
					SrcOrg:          srcOrg,
					SrcRepo:         repoName,
					SrcTag:          tagName,
					DstOrg:          cfg.PromoteTargetOrg,
					DstRepo:         repoName,
					DstTag:          tagName,
					ActorUserID:     nil,
					Note:            note,
					CreateIfMissing: true,
				})
				if err != nil {
					if errors.Is(err, repository.ErrImmutableTag) {
						// The one place we don't fail the whole op: an immutable
						// destination tag is logged + skipped so the rest of the
						// merge promotion proceeds.
						slog.WarnContext(ctx, "prregistry: skipping immutable dest tag on promote",
							"repo", repoName, "tag", tagName, "target_org", cfg.PromoteTargetOrg)
						continue
					}
					return fmt.Errorf("promote %s/%s:%s -> %s/%s:%s: %w",
						srcOrg, repoName, tagName, cfg.PromoteTargetOrg, repoName, tagName, err)
				}
			}
			// Keyset pagination: a short page is the last one.
			if len(tags) < tagPageSize {
				break
			}
			last = tags[len(tags)-1].GetName()
		}
	}
	return nil
}

// publish emits a best-effort lifecycle event. A publish failure is logged but
// never fails the request — the lifecycle mutation already committed, so the
// event is an at-most-once notification, not a transactional guarantee. The
// payload is any of the FUT-023 *Payload structs.
func (s *Service) publish(ctx context.Context, tenantID uuid.UUID, routingKey string, payload any) {
	if s.pub == nil {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		slog.WarnContext(ctx, "prregistry: marshal event payload failed (best-effort)",
			"routing_key", routingKey, "error", err)
		return
	}
	evt := events.Event{
		ID:         uuid.New().String(),
		Type:       routingKey,
		TenantID:   tenantID.String(),
		OccurredAt: time.Now().UTC(),
		Version:    eventVersion,
		Payload:    body,
	}
	pubCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if err := s.pub.Publish(pubCtx, routingKey, evt); err != nil {
		slog.WarnContext(ctx, "prregistry: publish failed (best-effort)",
			"routing_key", routingKey, "error", err)
	}
}
