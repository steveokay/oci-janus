//go:build integration

// Integration coverage for the FE-API-038 retention evaluator. The evaluator
// is "pure-ish" — the SQL load is one query, the rule application is plain
// Go on the result slice — but the failure modes that bite (dangling tag
// inference, COALESCE'd tag arrays, image_size_bytes nullability) only
// surface against a real Postgres. So this lives in the integration suite.
package integration

import (
	"context"
	"testing"
	"time"

	"github.com/steveokay/oci-janus/services/metadata/internal/repository"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
)

// seedRetentionEvalRepo creates the org + repo backing every eval test.
// Distinct name from seedRetentionRepo so two tests can run in parallel
// (each on its own container) without name collisions.
func seedRetentionEvalRepo(t *testing.T, repo *repository.Repository) string {
	t.Helper()
	ctx := context.Background()
	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "retention-eval-org")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	r, err := repo.CreateRepository(ctx, devTenantID, orgID, "retention-eval-repo", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	return r.GetRepoId()
}

// seedManifest writes a single manifest at an explicit created_at + size
// + tag list. The Repository public surface doesn't expose the timing knob
// (PutManifest stamps NOW()), so we drop into raw SQL via the same pool —
// the integration tests own the database for the duration of the test.
//
// digestSuffix is appended to a fixed prefix so each call produces a
// distinct digest without the caller having to hand-build 64 hex chars.
func seedManifest(
	t *testing.T,
	repo *repository.Repository,
	repoID, digestSuffix string,
	createdAt time.Time,
	sizeBytes int64,
	tagNames []string,
) string {
	t.Helper()
	ctx := context.Background()
	digest := "sha256:" + padTo64(digestSuffix)

	// Insert the manifest directly via the pool. Reaching past the public
	// API is intentional for seeding — we need control over created_at,
	// which PutManifest doesn't expose.
	if err := repo.RawInsertManifestForTest(ctx, repoID, devTenantID, digest, "application/vnd.oci.image.manifest.v1+json", sizeBytes, createdAt); err != nil {
		t.Fatalf("insert manifest: %v", err)
	}
	for _, tag := range tagNames {
		if _, err := repo.PutTag(ctx, devTenantID, repoID, tag, digest); err != nil {
			t.Fatalf("put tag %q: %v", tag, err)
		}
	}
	return digest
}

// padTo64 zero-extends a short hex-ish string to the 64-char sha256 hex
// length expected by the digest validator.
func padTo64(suffix string) string {
	const target = 64
	if len(suffix) >= target {
		return suffix[:target]
	}
	out := make([]byte, target)
	for i := range out {
		out[i] = '0'
	}
	copy(out[target-len(suffix):], suffix)
	return string(out)
}

// TestEvaluateRetention_emptyRepo_returnsEmptyResult covers the
// "I just created an empty repo and want to preview a future policy" case —
// the spec says NOT to return NotFound, just an empty result.
func TestEvaluateRetention_emptyRepo_returnsEmptyResult(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled: true,
		Rules:   []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if len(got.WouldDelete) != 0 || got.TotalCount != 0 || got.TotalBytes != 0 {
		t.Errorf("expected empty result, got %+v", got)
	}
}

// TestEvaluateRetention_allProtected_skipsAll seeds three manifests where
// every one carries a "latest-*" tag, then runs a policy that protects
// anything matching "latest" — every manifest goes into protected_skipped and
// the would-delete list is empty.
//
// NOTE: the tag names must be DISTINCT. tags carries UNIQUE(repo_id, name)
// (migration 00001) and PutTag upserts via ON CONFLICT (repo_id, name) DO
// UPDATE, so seeding three manifests all named "latest" would leave the tag
// pointing at only the last manifest and orphan the other two — they'd match
// max_age_days and land in would_delete, not protected_skipped. Distinct
// "latest-a/b/c" tags all match the unanchored "latest" pattern (MatchString
// is a substring match), so all three are correctly protected.
func TestEvaluateRetention_allProtected_skipsAll(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	seedManifest(t, repo, repoID, "aaa", now.Add(-100*24*time.Hour), 100, []string{"latest-a"})
	seedManifest(t, repo, repoID, "bbb", now.Add(-50*24*time.Hour), 200, []string{"latest-b"})
	seedManifest(t, repo, repoID, "ccc", now.Add(-10*24*time.Hour), 300, []string{"latest-c"})
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled:              true,
		Rules:                []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
		ProtectedTagPatterns: []string{"latest"},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if len(got.WouldDelete) != 0 {
		t.Errorf("expected zero deletions, got %d", len(got.WouldDelete))
	}
	if len(got.ProtectedSkipped) != 3 {
		t.Errorf("expected 3 protected, got %d", len(got.ProtectedSkipped))
	}
}

// TestEvaluateRetention_maxAgeDays_onlyOldSelected verifies a single
// max_age_days(30) rule selects only manifests pushed >30d ago.
func TestEvaluateRetention_maxAgeDays_onlyOldSelected(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	old1 := seedManifest(t, repo, repoID, "aaa", now.Add(-100*24*time.Hour), 100, []string{"old-1"})
	_ = seedManifest(t, repo, repoID, "bbb", now.Add(-10*24*time.Hour), 200, []string{"recent"})
	old2 := seedManifest(t, repo, repoID, "ccc", now.Add(-60*24*time.Hour), 300, []string{"old-2"})
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled: true,
		Rules:   []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if len(got.WouldDelete) != 2 {
		t.Fatalf("expected 2 selected, got %d", len(got.WouldDelete))
	}
	digests := map[string]bool{}
	for _, c := range got.WouldDelete {
		digests[c.ManifestDigest] = true
		if len(c.Reasons) != 1 || c.Reasons[0] != "max_age_days" {
			t.Errorf("expected only max_age_days reason, got %v", c.Reasons)
		}
	}
	if !digests[old1] || !digests[old2] {
		t.Errorf("missing expected digests: %v", digests)
	}
	if got.TotalCount != 2 || got.TotalBytes != 400 {
		t.Errorf("totals wrong: count=%d bytes=%d", got.TotalCount, got.TotalBytes)
	}
}

// TestEvaluateRetention_maxCount_keepsNewestN seeds 5 manifests and asks
// max_count(2) — the 3 oldest should be selected.
func TestEvaluateRetention_maxCount_keepsNewestN(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	digests := make([]string, 0, 5)
	// Push five manifests, newest last.
	for i := 0; i < 5; i++ {
		d := seedManifest(t, repo, repoID,
			"m"+string('a'+byte(i)),
			now.Add(-time.Duration(50-i*5)*24*time.Hour),
			100,
			[]string{"t-" + string('a'+byte(i))},
		)
		digests = append(digests, d)
	}
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled: true,
		Rules:   []*metadatav1.RetentionRule{{Kind: "max_count", Value: 2}},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if len(got.WouldDelete) != 3 {
		t.Fatalf("expected 3 selected (5 - max_count(2)), got %d", len(got.WouldDelete))
	}
	// Expect the OLDEST three (digests[0..2]) — newest two should be kept.
	selected := map[string]bool{}
	for _, c := range got.WouldDelete {
		selected[c.ManifestDigest] = true
	}
	for i, d := range digests {
		want := i < 3
		if selected[d] != want {
			t.Errorf("digest %s (idx %d) selected=%v, want %v", d, i, selected[d], want)
		}
	}
}

// TestEvaluateRetention_maxSizeBytes_atBoundary verifies the running-sum
// boundary: with cap=300 and three 100-byte manifests, the third one is
// the first selected (newest two are kept, sum = 200 ≤ 300; third pushes
// running sum to 300 BEFORE inclusion, so it's selected? — re-read the
// algorithm: rule selects every manifest AFTER running sum exceeds cap.
// With cap=300 and three 100-byte manifests, sum after the 3rd is 300
// which does NOT exceed cap; so the 4th would be the first selected.
// Test with three 200-byte manifests and cap=300: 200, 400 (>300), so the
// 2nd is the first selected.
func TestEvaluateRetention_maxSizeBytes_atBoundary(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	// 3 manifests, 200 bytes each, newest last.
	for i := 0; i < 3; i++ {
		seedManifest(t, repo, repoID,
			"s"+string('a'+byte(i)),
			now.Add(-time.Duration(30-i*5)*24*time.Hour),
			200,
			[]string{"sz-" + string('a'+byte(i))},
		)
	}
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled: true,
		Rules:   []*metadatav1.RetentionRule{{Kind: "max_size_bytes", Value: 300}},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	// Newest (first in DESC order) = 200 bytes (running 200, ≤300, kept).
	// Second = 200 bytes (running 400, >300 was set AFTER add, so second is kept too).
	// Actually re-walking the algorithm:
	//   i=0: running=0, 0 < 300 → not selected; running += 200 = 200
	//   i=1: running=200, 200 < 300 → not selected; running += 200 = 400
	//   i=2: running=400, 400 ≥ 300 → selected
	// So only the OLDEST manifest is selected.
	if len(got.WouldDelete) != 1 {
		t.Fatalf("expected 1 selected, got %d (would_delete=%+v)", len(got.WouldDelete), got.WouldDelete)
	}
	if got.WouldDelete[0].Reasons[0] != "max_size_bytes" {
		t.Errorf("expected max_size_bytes reason, got %v", got.WouldDelete[0].Reasons)
	}
}

// TestEvaluateRetention_danglingGraceDays_onlyUntaggedOld verifies the
// dangling rule selects only untagged manifests older than the threshold,
// using the documented "created_at as proxy" approximation.
func TestEvaluateRetention_danglingGraceDays_onlyUntaggedOld(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	// Old + tagged → not selected.
	seedManifest(t, repo, repoID, "tag1", now.Add(-100*24*time.Hour), 100, []string{"tagged-old"})
	// Old + dangling → selected.
	danglingOld := seedManifest(t, repo, repoID, "dng1", now.Add(-100*24*time.Hour), 200, nil)
	// New + dangling → NOT selected (too new).
	seedManifest(t, repo, repoID, "dng2", now.Add(-1*24*time.Hour), 300, nil)
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled: true,
		Rules:   []*metadatav1.RetentionRule{{Kind: "dangling_grace_days", Value: 30}},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if len(got.WouldDelete) != 1 {
		t.Fatalf("expected 1 selected, got %d", len(got.WouldDelete))
	}
	if got.WouldDelete[0].ManifestDigest != danglingOld {
		t.Errorf("wrong digest selected: got %s want %s", got.WouldDelete[0].ManifestDigest, danglingOld)
	}
}

// TestEvaluateRetention_multipleRules_collectsAllReasons verifies that a
// manifest matching both max_age_days AND max_count carries both kinds in
// reasons[]. This is the load-bearing composition guarantee from the spec.
func TestEvaluateRetention_multipleRules_collectsAllReasons(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	// Seed 3 manifests, all old (>30 days). max_age_days(30) selects all 3,
	// max_count(1) selects the 2 oldest, so the oldest two have BOTH
	// reasons, the newest has only max_age_days.
	d0 := seedManifest(t, repo, repoID, "mr1", now.Add(-100*24*time.Hour), 100, []string{"oldest"})
	d1 := seedManifest(t, repo, repoID, "mr2", now.Add(-90*24*time.Hour), 100, []string{"mid"})
	d2 := seedManifest(t, repo, repoID, "mr3", now.Add(-80*24*time.Hour), 100, []string{"newest"})
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled: true,
		Rules: []*metadatav1.RetentionRule{
			{Kind: "max_age_days", Value: 30},
			{Kind: "max_count", Value: 1},
		},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if len(got.WouldDelete) != 3 {
		t.Fatalf("expected 3 selected, got %d", len(got.WouldDelete))
	}
	byDigest := map[string][]string{}
	for _, c := range got.WouldDelete {
		byDigest[c.ManifestDigest] = c.Reasons
	}
	// d2 = newest of the three; max_count(1) keeps the newest 1 = d2, so
	// d2 has only max_age_days, while d0 + d1 have both.
	if got := byDigest[d2]; len(got) != 1 || got[0] != "max_age_days" {
		t.Errorf("newest reasons wrong: got %v want [max_age_days]", got)
	}
	for _, d := range []string{d0, d1} {
		got := byDigest[d]
		if len(got) != 2 || got[0] != "max_age_days" || got[1] != "max_count" {
			t.Errorf("digest %s reasons wrong: got %v want [max_age_days max_count] (sorted)", d, got)
		}
	}
}

// TestEvaluateRetention_truncation_setsFlagAndTotals seeds more candidates
// than the cap and verifies (a) the slice is truncated, (b) the flag is
// set, (c) totals reflect the FULL set.
func TestEvaluateRetention_truncation_setsFlagAndTotals(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	// Seed 5 old manifests so the rule selects all of them.
	for i := 0; i < 5; i++ {
		seedManifest(t, repo, repoID,
			"tr"+string('a'+byte(i)),
			now.Add(-100*24*time.Hour),
			100,
			[]string{"tr-" + string('a'+byte(i))},
		)
	}
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled: true,
		Rules:   []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
	}
	// Cap at 2 — expect truncation flag + total_count=5.
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 2, 100)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if !got.Truncated {
		t.Error("expected truncated=true")
	}
	if len(got.WouldDelete) != 2 {
		t.Errorf("expected 2 in slice, got %d", len(got.WouldDelete))
	}
	if got.TotalCount != 5 {
		t.Errorf("expected total_count=5, got %d", got.TotalCount)
	}
	if got.TotalBytes != 500 {
		t.Errorf("expected total_bytes=500, got %d", got.TotalBytes)
	}
}

// FE-API-043 max_idle_days rule. The combined gate (created_at < cutoff AND
// (last_pulled_at IS NULL OR last_pulled_at < cutoff)) is the load-bearing
// safety property — without it, every brand-new push with NULL
// last_pulled_at would match immediately. Each test below pins one corner
// of that gate.

// TestEvaluateRetention_maxIdleDays_idleAndOld_selected: pulled 60d ago,
// created 90d ago, threshold 30d → selected with reason "max_idle_days".
func TestEvaluateRetention_maxIdleDays_idleAndOld_selected(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	digest := seedManifest(t, repo, repoID, "id1", now.Add(-90*24*time.Hour), 100, []string{"idle-old"})
	if err := repo.SetManifestLastPulledForTest(context.Background(), repoID, devTenantID, digest, now.Add(-60*24*time.Hour)); err != nil {
		t.Fatalf("seed last_pulled_at: %v", err)
	}
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled: true,
		Rules:   []*metadatav1.RetentionRule{{Kind: "max_idle_days", Value: 30}},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if len(got.WouldDelete) != 1 {
		t.Fatalf("expected 1 selection, got %d (%+v)", len(got.WouldDelete), got.WouldDelete)
	}
	if c := got.WouldDelete[0]; c.ManifestDigest != digest ||
		len(c.Reasons) != 1 || c.Reasons[0] != "max_idle_days" {
		t.Errorf("unexpected candidate: %+v", c)
	}
}

// TestEvaluateRetention_maxIdleDays_recentPullOldManifest_notSelected: a
// 90-day-old manifest pulled 5 days ago is NOT idle — recent pull keeps it.
func TestEvaluateRetention_maxIdleDays_recentPullOldManifest_notSelected(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	digest := seedManifest(t, repo, repoID, "id2", now.Add(-90*24*time.Hour), 100, []string{"recent-pull"})
	if err := repo.SetManifestLastPulledForTest(context.Background(), repoID, devTenantID, digest, now.Add(-5*24*time.Hour)); err != nil {
		t.Fatalf("seed last_pulled_at: %v", err)
	}
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled: true,
		Rules:   []*metadatav1.RetentionRule{{Kind: "max_idle_days", Value: 30}},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if len(got.WouldDelete) != 0 {
		t.Errorf("expected no selections (recent pull), got %+v", got.WouldDelete)
	}
}

// TestEvaluateRetention_maxIdleDays_neverPulledBrandNew_notSelected: pins the
// chicken-and-egg gate. NULL last_pulled_at + created 5d ago + threshold 30d
// → NOT selected (the manifest hasn't been around long enough to have been
// pulled yet — otherwise deploying max_idle_days would nuke every recent
// push that hasn't been pulled in its first few days).
func TestEvaluateRetention_maxIdleDays_neverPulledBrandNew_notSelected(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	// No SetManifestLastPulledForTest call → last_pulled_at remains NULL.
	seedManifest(t, repo, repoID, "id3", now.Add(-5*24*time.Hour), 100, []string{"new-never-pulled"})
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled: true,
		Rules:   []*metadatav1.RetentionRule{{Kind: "max_idle_days", Value: 30}},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if len(got.WouldDelete) != 0 {
		t.Errorf("expected no selections (brand-new + NULL), got %+v", got.WouldDelete)
	}
}

// TestEvaluateRetention_maxIdleDays_neverPulledOldEnough_selected: NULL
// last_pulled_at + created 60d ago + threshold 30d → selected. Captures the
// "existed pre-pull-tracking and was never touched" case the gate explicitly
// keeps actionable.
func TestEvaluateRetention_maxIdleDays_neverPulledOldEnough_selected(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	digest := seedManifest(t, repo, repoID, "id4", now.Add(-60*24*time.Hour), 100, []string{"old-never-pulled"})
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled: true,
		Rules:   []*metadatav1.RetentionRule{{Kind: "max_idle_days", Value: 30}},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if len(got.WouldDelete) != 1 {
		t.Fatalf("expected 1 selection, got %d (%+v)", len(got.WouldDelete), got.WouldDelete)
	}
	if c := got.WouldDelete[0]; c.ManifestDigest != digest ||
		len(c.Reasons) != 1 || c.Reasons[0] != "max_idle_days" {
		t.Errorf("unexpected candidate: %+v", c)
	}
}

// TestEvaluateRetention_maxIdleDaysAndMaxAgeDays_collectsBothReasons: verifies
// OR composition with another date-based rule — a manifest matching both
// rules carries both kinds in reasons[] (sorted).
func TestEvaluateRetention_maxIdleDaysAndMaxAgeDays_collectsBothReasons(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	digest := seedManifest(t, repo, repoID, "id5", now.Add(-90*24*time.Hour), 100, []string{"old-idle"})
	if err := repo.SetManifestLastPulledForTest(context.Background(), repoID, devTenantID, digest, now.Add(-60*24*time.Hour)); err != nil {
		t.Fatalf("seed last_pulled_at: %v", err)
	}
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled: true,
		Rules: []*metadatav1.RetentionRule{
			{Kind: "max_age_days", Value: 30},
			{Kind: "max_idle_days", Value: 30},
		},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if len(got.WouldDelete) != 1 {
		t.Fatalf("expected 1 selection, got %d (%+v)", len(got.WouldDelete), got.WouldDelete)
	}
	c := got.WouldDelete[0]
	// Reasons[] is sorted ascending by the evaluator — verify both kinds and
	// stable ordering.
	if len(c.Reasons) != 2 || c.Reasons[0] != "max_age_days" || c.Reasons[1] != "max_idle_days" {
		t.Errorf("expected reasons [max_age_days max_idle_days], got %v", c.Reasons)
	}
}

// TestEvaluateRetention_maxIdleDays_protectedWins: a manifest that matches
// max_idle_days but carries a protected tag goes to protected_skipped, not
// would_delete. Protection runs first, regardless of rule kind.
func TestEvaluateRetention_maxIdleDays_protectedWins(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	digest := seedManifest(t, repo, repoID, "id6", now.Add(-90*24*time.Hour), 100, []string{"latest"})
	if err := repo.SetManifestLastPulledForTest(context.Background(), repoID, devTenantID, digest, now.Add(-60*24*time.Hour)); err != nil {
		t.Fatalf("seed last_pulled_at: %v", err)
	}
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled:              true,
		Rules:                []*metadatav1.RetentionRule{{Kind: "max_idle_days", Value: 30}},
		ProtectedTagPatterns: []string{"latest"},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if len(got.WouldDelete) != 0 {
		t.Errorf("expected zero deletions (tag protected), got %+v", got.WouldDelete)
	}
	if len(got.ProtectedSkipped) != 1 || got.ProtectedSkipped[0].ManifestDigest != digest {
		t.Errorf("expected the manifest to be in protected_skipped, got %+v", got.ProtectedSkipped)
	}
}

// TestEvaluateRetention_maxIdleDays_boundaryExact: last_pulled_at exactly N
// days ago with rule N. Pinned semantics: strict "<" (Before) — at exactly
// the boundary the manifest is KEPT, only strictly older than N days counts
// as idle. Matches max_age_days' boundary so the two date-based rules are
// internally consistent.
func TestEvaluateRetention_maxIdleDays_boundaryExact(t *testing.T) {
	repo := buildRepo(t)
	repoID := seedRetentionEvalRepo(t, repo)
	now := time.Now().UTC()
	digest := seedManifest(t, repo, repoID, "id7", now.Add(-60*24*time.Hour), 100, []string{"boundary"})
	// Seed last_pulled_at very slightly INSIDE the 30-day window (a few
	// seconds shy of exactly 30 days ago). The test does not assert "exactly
	// 30 days" because the evaluator captures `now` after seeding, which
	// would race; instead the seeded timestamp is recent enough that the
	// cutoff (now - 30d) is strictly older than it → NOT idle.
	pulledAt := now.Add(-30*24*time.Hour + 5*time.Second)
	if err := repo.SetManifestLastPulledForTest(context.Background(), repoID, devTenantID, digest, pulledAt); err != nil {
		t.Fatalf("seed last_pulled_at: %v", err)
	}
	ctx := context.Background()

	cand := &metadatav1.RetentionPolicyCandidate{
		Enabled: true,
		Rules:   []*metadatav1.RetentionRule{{Kind: "max_idle_days", Value: 30}},
	}
	got, err := repo.EvaluateRetention(ctx, devTenantID, repoID, cand, 0, 0)
	if err != nil {
		t.Fatalf("EvaluateRetention: %v", err)
	}
	if len(got.WouldDelete) != 0 {
		t.Errorf("boundary: pulled within window must NOT be selected, got %+v", got.WouldDelete)
	}
}
