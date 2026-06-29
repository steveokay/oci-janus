package exportworker

// probe.go — adapter that satisfies the handler.AuditExportDLXProbe
// interface without forcing the handler package to import this
// package (which would create a circular import via the consumer
// adapter that already lives next to it).
//
// The struct bundles the MgmtClient + a Drain closure so the audit
// server can wire both observability + admin paths from a single
// call: `handler.WithExportDLXProbe(exportworker.NewProbe(rabbitURL))`.

import (
	"context"

	"github.com/google/uuid"
)

// Probe satisfies the handler-side interface for live DLX depth +
// drain. Stored on the GRPCHandler at boot time; nil-safe for
// dev/test stacks without RabbitMQ.
type Probe struct {
	rabbitURL string
	mgmt      *MgmtClient
}

// NewProbe builds a probe. Returns nil + nil when rabbitURL is empty
// (so the audit server's server.go bootstrap stays a single happy
// path — no nil-checks at the call site).
func NewProbe(rabbitURL, mgmtURL string) (*Probe, error) {
	if rabbitURL == "" {
		return nil, nil
	}
	mc, err := NewMgmtClient(rabbitURL, mgmtURL)
	if err != nil {
		return nil, err
	}
	return &Probe{rabbitURL: rabbitURL, mgmt: mc}, nil
}

// QueueDepth returns the live depth of `audit.export.dlx`. -1 + nil
// when the Mgmt API returns an error (we don't want to fail the
// GetAuditExportConfig RPC over an observability gap — the FE
// surfaces "depth unknown" instead). The narrow int32 type is for
// the proto wire shape; we cap above MaxInt32 because no SIEM outage
// will produce 2B+ stuck events before someone notices.
func (p *Probe) QueueDepth(ctx context.Context) (int32, error) {
	if p == nil || p.mgmt == nil {
		return -1, nil
	}
	d, err := p.mgmt.QueueDepth(ctx, QueueAuditExportDLX)
	if err != nil {
		// Intentional: return -1 sentinel (not the error) so the gauge metric
		// reports an obvious "unknown" value without breaking the probe loop.
		// The mgmt-API failure is logged at the caller via the slog warn path.
		return -1, nil //nolint:nilerr
	}
	if d > 2_000_000_000 {
		d = 2_000_000_000
	}
	return int32(d), nil
}

// Drain re-publishes parked messages for this tenant. Wraps the
// package-level Drain function so the handler test suite can stub
// the interface without spinning up a real broker.
func (p *Probe) Drain(ctx context.Context, tenantID uuid.UUID) (int32, error) {
	if p == nil {
		return 0, nil
	}
	n, err := Drain(ctx, p.rabbitURL, tenantID)
	if n > 2_000_000_000 {
		n = 2_000_000_000
	}
	return int32(n), err
}
