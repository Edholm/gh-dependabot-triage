package server

import (
	"strings"
	"testing"
	"time"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
	"gotest.tools/v3/assert"
)

func TestReviewBadge(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		decision ghclient.ReviewDecision
		want     badge
	}{
		{ghclient.ReviewApproved, badge{"Approved", classGood}},
		{ghclient.ReviewChangesRequested, badge{"Changes requested", classBad}},
		{ghclient.ReviewRequired, badge{"Review required", classWarn}},
		{"", badge{"None", classNeutral}},
	} {
		t.Run(string(tt.decision), func(t *testing.T) {
			t.Parallel()
			if got := reviewBadge(tt.decision); got != tt.want {
				t.Errorf("reviewBadge(%q) = %v, want %v", tt.decision, got, tt.want)
			}
		})
	}
}

func TestStatusBadge(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		pr   ghclient.PullRequest
		want badge
	}{
		{
			"draft flag wins",
			ghclient.PullRequest{Draft: true, MergeState: ghclient.MergeStateClean},
			badge{"Draft", classNeutral},
		},
		{"draft state", ghclient.PullRequest{MergeState: ghclient.MergeStateDraft}, badge{"Draft", classNeutral}},
		{"conflicts", ghclient.PullRequest{MergeState: ghclient.MergeStateDirty}, badge{"Conflicts", classBad}},
		{"behind", ghclient.PullRequest{MergeState: ghclient.MergeStateBehind}, badge{"Behind", classWarn}},
		{"blocked", ghclient.PullRequest{MergeState: ghclient.MergeStateBlocked}, badge{"Blocked", classWarn}},
		{"unstable", ghclient.PullRequest{MergeState: ghclient.MergeStateUnstable}, badge{"Unstable", classWarn}},
		{"clean", ghclient.PullRequest{MergeState: ghclient.MergeStateClean}, badge{"Ready", classGood}},
		{"has hooks", ghclient.PullRequest{MergeState: ghclient.MergeStateHasHooks}, badge{"Ready", classGood}},
		{"unknown", ghclient.PullRequest{MergeState: ghclient.MergeStateUnknown}, badge{"Unknown", classNeutral}},
		{"not yet computed", ghclient.PullRequest{}, badge{"Unknown", classNeutral}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := statusBadge(tt.pr); got != tt.want {
				t.Errorf("statusBadge(%+v) = %v, want %v", tt.pr, got, tt.want)
			}
		})
	}
}

func TestCIBadge(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		state ghclient.CheckState
		want  badge
	}{
		{ghclient.CheckSuccess, badge{"✓ Passing", classGood}},
		{ghclient.CheckFailure, badge{"✗ Failing", classBad}},
		{ghclient.CheckError, badge{"✗ Failing", classBad}},
		{ghclient.CheckPending, badge{"● Running", classWarn}},
		{ghclient.CheckExpected, badge{"● Running", classWarn}},
		{"", badge{"– No checks", classNeutral}},
	} {
		t.Run(string(tt.state), func(t *testing.T) {
			t.Parallel()
			if got := ciBadge(tt.state); got != tt.want {
				t.Errorf("ciBadge(%q) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

func TestNewRows_SplitsRepositoryIntoOwnerAndName(t *testing.T) {
	t.Parallel()

	rows := newRows(time.Now(), []ghclient.PullRequest{
		{Repository: "acme/api", Number: 1},
		{Repository: "no-owner", Number: 2},
	}, pageQuery{order: defaultOrder()})

	assert.Equal(t, len(rows), 2)
	assert.DeepEqual(t, []string{rows[0].Repository, rows[0].Owner, rows[0].Name}, []string{"acme/api", "acme", "api"})
	assert.DeepEqual(t, []string{rows[1].Repository, rows[1].Owner, rows[1].Name}, []string{"no-owner", "", "no-owner"})
}

func TestTooltip(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		text string
		want string
	}{
		{"empty", "", ""},
		{"blank", " \n\t\n ", ""},
		{"short text is unchanged", "Bumps lib.\nRelease notes", "Bumps lib.\nRelease notes"},
		{"surrounding whitespace is trimmed", "\n  Bumps lib.  \n\n", "Bumps lib."},
		{"runs of blank lines collapse", "one\n\n\n\n\ntwo\n \nthree", "one\n\ntwo\n\nthree"},
		{"carriage returns are dropped", "one\r\ntwo\r\n", "one\ntwo"},
		{
			"long text is cut",
			strings.Repeat("a", maxTooltipRunes+1),
			strings.Repeat("a", maxTooltipRunes) + "…",
		},
		{"text at the limit is unchanged", strings.Repeat("a", maxTooltipRunes), strings.Repeat("a", maxTooltipRunes)},
		{
			"runes are not split",
			strings.Repeat("å", maxTooltipRunes+1),
			strings.Repeat("å", maxTooltipRunes) + "…",
		},
		{
			"whitespace before the cut is dropped",
			strings.Repeat("a", maxTooltipRunes-1) + " bcd",
			strings.Repeat("a", maxTooltipRunes-1) + "…",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tooltip(tt.text); got != tt.want {
				t.Errorf("tooltip(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestExtraBadges(t *testing.T) {
	t.Parallel()

	assert.Equal(t, len(extraBadges(ghclient.PullRequest{})), 0)

	got := extraBadges(ghclient.PullRequest{AutoMerge: true, MergeQueue: &ghclient.MergeQueueEntry{Position: 3}})
	assert.DeepEqual(t, got, []badge{{"Auto-merge", classInfo}, {"In merge queue #3", classInfo}})
}

func TestRelativeAge(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "now"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{26 * time.Hour, "1d"},
		{6 * 24 * time.Hour, "6d"},
		{15 * 24 * time.Hour, "2w"},
		{400 * 24 * time.Hour, "1y"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := relativeAge(now, now.Add(-tt.ago)); got != tt.want {
				t.Errorf("relativeAge(now, now-%s) = %q, want %q", tt.ago, got, tt.want)
			}
		})
	}
}

func TestNewRows_Behind(t *testing.T) {
	t.Parallel()
	behind := ghclient.PullRequest{Repository: "acme/api", Number: 1, MergeState: ghclient.MergeStateBehind}
	draft := ghclient.PullRequest{Repository: "acme/api", Number: 2, MergeState: ghclient.MergeStateBehind, Draft: true}
	clean := ghclient.PullRequest{Repository: "acme/api", Number: 3, MergeState: ghclient.MergeStateClean}

	rows := newRows(time.Now(), []ghclient.PullRequest{behind, draft, clean}, pageQuery{})

	got := make(map[int]bool)
	for _, r := range rows {
		got[r.Number] = r.Behind
	}
	assert.DeepEqual(t, got, map[int]bool{1: true, 2: false, 3: false})
}
