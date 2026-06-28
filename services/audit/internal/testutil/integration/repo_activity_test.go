//go:build integration

// Package integration exercises the audit repository against a real PostgreSQL
// container via testcontainers, applying all goose migrations so the runtime
// schema (including RLS policies and the FE-API-004 expression index) matches
// production.
package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
	auditmigrations "github.com/steveokay/oci-janus/services/audit/migrations"
)

// newRepo spins up a PostgreSQL 16 container, runs every audit migration, and
// returns a Repository backed by a fresh pool. The container is torn down
// automatically when t finishes.
func newRepo(t *testing.T) *repository.Repository {
	t.Helper()
	ctx := context.Background()

	dsn := containers.Postgres(t)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	// Apply migrations via goose using a single-conn database/sql handle.
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	t.Cleanup(func() { _ = sqlDB.Close() })

	goose.SetBaseFS(auditmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose.SetDialect: %v", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		t.Fatalf("goose.Up: %v", err)
	}

	return repository.New(pool)
}

// rawMetadata builds the {"event_id": ..., "raw": payload} JSON envelope that
// services/audit/internal/eventconsumer writes for every audit row.
func rawMetadata(t *testing.T, payload map[string]any) json.RawMessage {
	t.Helper()
	rawBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	wrapped, err := json.Marshal(map[string]any{
		"event_id": uuid.New().String(),
		"raw":      json.RawMessage(rawBytes),
	})
	if err != nil {
		t.Fatalf("marshal wrapped: %v", err)
	}
	return wrapped
}

// seed inserts one audit event row with the given fields.
func seed(t *testing.T, repo *repository.Repository, tenant uuid.UUID, action, repoName, tag, digest string, when time.Time, outcome string) {
	t.Helper()
	meta := rawMetadata(t, map[string]any{
		"repository_name": repoName,
		"tag":             tag,
		"manifest_digest": digest,
		"pushed_by":       "alice",
	})
	if outcome == "" {
		outcome = "success"
	}
	err := repo.Insert(context.Background(), &repository.AuditEvent{
		TenantID:   tenant,
		ActorID:    "alice",
		ActorType:  "user",
		Action:     action,
		Resource:   repoName + ":" + tag,
		Outcome:    outcome,
		Metadata:   meta,
		OccurredAt: when,
	})
	if err != nil {
		t.Fatalf("seed insert: %v", err)
	}
}

func TestRepoActivity_filtersByTenantAndRepo(t *testing.T) {
	repo := newRepo(t)
	tenantA := uuid.New()
	tenantB := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Three events for tenantA + myorg/myrepo, plus distractors that must not
	// appear in the result: a sibling repo, a different tenant, and an event
	// whose payload lacks repository_name (mirrors tenant.created in prod).
	seed(t, repo, tenantA, "push.image", "myorg/myrepo", "v1", "sha256:111", now, "success")
	seed(t, repo, tenantA, "scan.completed", "myorg/myrepo", "", "sha256:222", now.Add(-time.Minute), "success")
	seed(t, repo, tenantA, "image.signed", "myorg/myrepo", "v1", "sha256:111", now.Add(-2*time.Minute), "success")

	seed(t, repo, tenantA, "push.image", "myorg/other", "v1", "sha256:abc", now, "success")  // sibling repo
	seed(t, repo, tenantB, "push.image", "myorg/myrepo", "v1", "sha256:def", now, "success") // wrong tenant

	defaults := []string{
		"push.image", "delete.manifest", "delete.tag",
		"scan.completed", "scan.policy_blocked", "image.signed",
	}
	rows, err := repo.GetRepoActivity(
		context.Background(),
		tenantA,
		"myorg/myrepo",
		now.Add(-time.Hour),
		time.Time{}, uuid.Nil,
		defaults,
		50,
	)
	if err != nil {
		t.Fatalf("GetRepoActivity: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows for myorg/myrepo, got %d", len(rows))
	}
	// Newest-first ordering.
	if !rows[0].OccurredAt.Equal(now) {
		t.Errorf("expected newest row first, got %v", rows[0].OccurredAt)
	}
}

func TestRepoActivity_eventTypeFilter(t *testing.T) {
	repo := newRepo(t)
	tenant := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)

	seed(t, repo, tenant, "push.image", "myorg/myrepo", "a", "sha256:1", now, "success")
	seed(t, repo, tenant, "scan.completed", "myorg/myrepo", "", "sha256:2", now.Add(-time.Minute), "success")
	seed(t, repo, tenant, "image.signed", "myorg/myrepo", "a", "sha256:1", now.Add(-2*time.Minute), "success")

	rows, err := repo.GetRepoActivity(
		context.Background(),
		tenant,
		"myorg/myrepo",
		now.Add(-time.Hour),
		time.Time{}, uuid.Nil,
		[]string{"push.image"},
		50,
	)
	if err != nil {
		t.Fatalf("GetRepoActivity: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 push.image row, got %d", len(rows))
	}
	if rows[0].Action != "push.image" {
		t.Errorf("expected action push.image, got %q", rows[0].Action)
	}
}

func TestRepoActivity_pagination(t *testing.T) {
	repo := newRepo(t)
	tenant := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Five events, one per minute back from `now`.
	for i := 0; i < 5; i++ {
		seed(t, repo, tenant, "push.image", "myorg/myrepo",
			"v"+string(rune('a'+i)), "sha256:row", now.Add(-time.Duration(i)*time.Minute), "success")
	}

	first, err := repo.GetRepoActivity(
		context.Background(),
		tenant,
		"myorg/myrepo",
		now.Add(-time.Hour),
		time.Time{}, uuid.Nil,
		[]string{"push.image"},
		2,
	)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("page 1: expected 2 rows, got %d", len(first))
	}

	// Use the second row as the cursor and ask for the next page.
	cursor := first[1]
	second, err := repo.GetRepoActivity(
		context.Background(),
		tenant,
		"myorg/myrepo",
		now.Add(-time.Hour),
		cursor.OccurredAt, cursor.ID,
		[]string{"push.image"},
		10,
	)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(second) != 3 {
		t.Fatalf("page 2: expected 3 rows, got %d", len(second))
	}
	// No row from the second page should overlap with the first.
	for _, r := range second {
		for _, prev := range first {
			if r.ID == prev.ID {
				t.Errorf("page 2 leaked row %s already seen on page 1", r.ID)
			}
		}
	}
}

func TestRepoActivity_emptyEventTypes_returnsNoRows(t *testing.T) {
	repo := newRepo(t)
	tenant := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	seed(t, repo, tenant, "push.image", "myorg/myrepo", "a", "sha256:1", now, "success")

	rows, err := repo.GetRepoActivity(
		context.Background(),
		tenant,
		"myorg/myrepo",
		now.Add(-time.Hour),
		time.Time{}, uuid.Nil,
		nil,
		50,
	)
	if err != nil {
		t.Fatalf("GetRepoActivity: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows for nil event_types, got %d", len(rows))
	}
}
