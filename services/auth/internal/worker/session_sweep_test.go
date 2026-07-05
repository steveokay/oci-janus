package worker

import (
	"context"
	"testing"
	"time"
)

type fakeSweepRepo struct {
	deleted   int64
	gotCutoff time.Time
}

func (f *fakeSweepRepo) DeleteExpired(_ context.Context, idleCutoff time.Time) (int64, error) {
	f.gotCutoff = idleCutoff
	return f.deleted, nil
}

func TestSessionSweep_TickOnce(t *testing.T) {
	repo := &fakeSweepRepo{deleted: 3}
	w := NewSessionSweeper(repo, 14*24*time.Hour, time.Minute, nil)
	if err := w.TickOnce(context.Background()); err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	// The idle cutoff must be ~14 days in the past.
	if repo.gotCutoff.After(time.Now().Add(-13 * 24 * time.Hour)) {
		t.Fatal("idle cutoff should be ~14 days in the past")
	}
}
