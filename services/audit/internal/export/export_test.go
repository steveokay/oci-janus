package export

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// sampleEvent is a fully-populated audit event used across the renderer
// tests. ActorType is deliberately a multi-word value ("service_account")
// so the tests also guard that the field survives rendering intact.
func sampleEvent() Event {
	return Event{
		ID:         "f730d919-1111-2222-3333-444455556666",
		TenantID:   "98dbe36b-ef28-4903-b25c-bff1b2921c9e",
		ActorID:    "00000000-aaaa-bbbb-cccc-dddddddddddd",
		ActorType:  "service_account",
		ActorIP:    "203.0.113.42",
		Action:     "image.signed",
		Resource:   `{"repo":"dev/alpine","tag":"3.18"}`,
		Outcome:    "success",
		Metadata:   json.RawMessage(`{"raw":true}`),
		OccurredAt: time.Date(2026, 7, 12, 9, 48, 58, 0, time.UTC),
	}
}

// TestRenderCEF_includesActorType is the core assertion for the fix: the CEF
// body must carry actor_type on cs5 so a CEF/ArcSight pipeline retains the
// same "who-type" dimension the syslog SD block already carries. The shipped
// cs4=metadata_b64 mapping must be untouched (both coexist).
func TestRenderCEF_includesActorType(t *testing.T) {
	body := renderCEF(sampleEvent())

	// Header shape is unchanged (regression guard on the pipe-delimited head).
	if !strings.HasPrefix(body, "CEF:0|oci-janus|registry|1.0|image.signed|image.signed|") {
		t.Fatalf("unexpected CEF header: %q", body)
	}
	for _, want := range []string{
		"cs5Label=actor_type",
		"cs5=service_account",
		"suser=00000000-aaaa-bbbb-cccc-dddddddddddd", // actor_id dimension still present
		"cs1Label=tenant_id",
		"cs2Label=resource",
		"cs3Label=event_id",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("CEF body missing %q\ngot: %s", want, body)
		}
	}
}

// TestRenderCEF_actorTypeCoexistsWithMetadata proves cs5 (actor_type) and the
// conditional cs4 (metadata_b64) both appear — i.e. adding actor_type did not
// displace the existing metadata slot.
func TestRenderCEF_actorTypeCoexistsWithMetadata(t *testing.T) {
	evt := sampleEvent()
	body := renderCEF(evt)

	wantMeta := "cs4=" + base64.StdEncoding.EncodeToString(evt.Metadata)
	if !strings.Contains(body, "cs4Label=metadata_b64") || !strings.Contains(body, wantMeta) {
		t.Errorf("CEF body dropped metadata cs4\ngot: %s", body)
	}
	if !strings.Contains(body, "cs5=service_account") {
		t.Errorf("CEF body dropped actor_type cs5\ngot: %s", body)
	}
}

// TestRenderCEF_noMetadataOmitsCS4ButKeepsCS5 covers the empty-metadata path:
// cs4 is omitted (conditional) while cs5/actor_type remains (unconditional).
func TestRenderCEF_noMetadataOmitsCS4ButKeepsCS5(t *testing.T) {
	evt := sampleEvent()
	evt.Metadata = nil
	body := renderCEF(evt)

	if strings.Contains(body, "cs4") {
		t.Errorf("cs4 must be omitted when metadata is empty\ngot: %s", body)
	}
	if !strings.Contains(body, "cs5=service_account") {
		t.Errorf("cs5/actor_type must be present regardless of metadata\ngot: %s", body)
	}
}

// TestRenderSyslog_includesActorType documents + guards the parity target: the
// syslog SD block carries actor_type (it always has). This is the field CEF was
// previously missing and now matches.
func TestRenderSyslog_includesActorType(t *testing.T) {
	line := renderSyslog(sampleEvent())
	if !strings.Contains(line, `actor_type="service_account"`) {
		t.Errorf("syslog SD block missing actor_type\ngot: %s", line)
	}
}
