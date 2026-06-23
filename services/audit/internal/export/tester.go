package export

// tester.go — exposes the synthetic-event delivery path used by the
// `TestAuditExportConfig` gRPC RPC (futures.md Tier 1 #4). Keeps the
// renderer/transport code as the single source of truth — a Test
// shouldn't be able to "succeed" via a different code path than a
// real audit event.

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// Tester implements the handler.AuditExportTester contract. Wired
// from services/audit/internal/server/server.go at boot.
type Tester struct{}

// NewTester returns a Tester. No fields today — kept as a struct for
// future state (e.g. metrics counters specific to test events).
func NewTester() *Tester {
	return &Tester{}
}

// DeliverTest emits a synthetic audit_export.test event with the
// requested format / target / secrets, runs the same Deliver loop a
// real event would, and returns the rendered wire payload + the last
// error. The synthetic event uses a deterministic shape so an
// operator can recognise it in their SIEM ("hey, that one came from
// the Send-Test button").
func (t *Tester) DeliverTest(
	ctx context.Context,
	cfg *repository.AuditExportConfig,
	hmacSecret, bearerToken string,
) (string, error) {
	evt := Event{
		ID:         uuid.New().String(),
		TenantID:   cfg.TenantID.String(),
		ActorID:    "audit-export-test",
		ActorType:  "system",
		ActorIP:    "",
		Action:     "audit_export.test",
		Resource:   "",
		Outcome:    "success",
		OccurredAt: time.Now().UTC(),
	}
	expCfg := Config{
		TenantID:    cfg.TenantID.String(),
		Format:      cfg.Format,
		TargetURL:   cfg.TargetURL,
		HMACSecret:  hmacSecret,
		BearerToken: bearerToken,
	}
	return Deliver(ctx, expCfg, evt)
}
