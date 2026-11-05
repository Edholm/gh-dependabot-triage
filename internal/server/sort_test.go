package server

import (
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
	"gotest.tools/v3/assert"
)

func TestParseOrder(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		query string
		want  order
	}{
		{"no query", "", defaultOrder()},
		{"unknown column", "sort=nope&dir=desc", defaultOrder()},
		{"direction without column", "dir=desc", defaultOrder()},
		{"column ascending", "sort=ci&dir=asc", order{column: columnCI}},
		{"column descending", "sort=ci&dir=desc", order{column: columnCI, descending: true}},
		{"column without direction", "sort=ci", order{column: columnCI}},
		{"unknown direction", "sort=ci&dir=sideways", order{column: columnCI}},
		{"age starts with the oldest", "sort=age", order{column: columnAge, descending: true}},
		{"age ascending", "sort=age&dir=asc", order{column: columnAge}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			query, err := url.ParseQuery(tt.query)
			assert.NilError(t, err)
			if got := parseOrder(query); got != tt.want {
				t.Errorf("parseOrder(%q) = %+v, want %+v", tt.query, got, tt.want)
			}
		})
	}
}

func TestOrder_PathRoundTrips(t *testing.T) {
	t.Parallel()
	for _, c := range columns() {
		for _, descending := range []bool{false, true} {
			want := order{column: c, descending: descending}

			target, err := url.Parse(want.path("/"))
			assert.NilError(t, err)

			assert.Equal(t, parseOrder(target.Query()), want)
		}
	}
	assert.Equal(t, defaultOrder().path("/refresh"), "/refresh", "the default order needs no query")
}

func TestOrder_Clicked(t *testing.T) {
	t.Parallel()

	assert.Equal(t, defaultOrder().clicked(columnRepository), order{column: columnRepository, descending: true})
	assert.Equal(t, order{column: columnCI}.clicked(columnCI), order{column: columnCI, descending: true})
	assert.Equal(t, order{column: columnCI, descending: true}.clicked(columnCI), order{column: columnCI})
	assert.Equal(t, order{column: columnCI, descending: true}.clicked(columnReview), order{column: columnReview})
	assert.Equal(t, defaultOrder().clicked(columnAge), order{column: columnAge, descending: true})
}

func TestOrder_Headers(t *testing.T) {
	t.Parallel()

	headers := order{column: columnCI, descending: true}.headers()

	assert.DeepEqual(t, headers, []header{
		{Label: "Repository", Href: "/"},
		{Label: "Pull request", Href: "/?dir=asc&sort=title"},
		{Label: "Age", Href: "/?dir=desc&sort=age"},
		{Label: "Review", Href: "/?dir=asc&sort=review"},
		{Label: "Status", Href: "/?dir=asc&sort=status"},
		{Label: "CI", Href: "/?dir=asc&sort=ci", Sort: "descending", Arrow: "▼"},
	})
	assert.Equal(t, defaultOrder().headers()[0].Sort, "ascending")
}

func TestSortPullRequests(t *testing.T) {
	t.Parallel()
	now := testNow()
	prs := []ghclient.PullRequest{
		{
			Repository: "acme/b", Number: 1, Title: "bump zlib", CreatedAt: now.Add(-1 * time.Hour),
			ReviewDecision: ghclient.ReviewRequired, MergeState: ghclient.MergeStateBlocked, CI: ghclient.CheckFailure,
		},
		{
			Repository: "acme/a", Number: 2, Title: "Bump axios", CreatedAt: now.Add(-3 * time.Hour),
			ReviewDecision: ghclient.ReviewApproved, MergeState: ghclient.MergeStateClean, CI: ghclient.CheckPending,
		},
		{
			Repository:     "acme/a",
			Number:         1,
			Title:          "bump lodash",
			CreatedAt:      now.Add(-2 * time.Hour),
			ReviewDecision: ghclient.ReviewChangesRequested,
			MergeState:     ghclient.MergeStateDirty,
			CI:             ghclient.CheckSuccess,
		},
		{
			Repository: "acme/c", Number: 3, Title: "bump react", CreatedAt: now.Add(-4 * time.Hour),
			MergeState: ghclient.MergeStateUnknown,
		},
	}
	for _, tt := range []struct {
		name  string
		order order
		want  []string
	}{
		{"repository", order{column: columnRepository}, []string{"acme/a#1", "acme/a#2", "acme/b#1", "acme/c#3"}},
		{
			"repository descending, ties keep number ascending",
			order{column: columnRepository, descending: true},
			[]string{"acme/c#3", "acme/b#1", "acme/a#1", "acme/a#2"},
		},
		{"title ignores case", order{column: columnTitle}, []string{"acme/a#2", "acme/a#1", "acme/c#3", "acme/b#1"}},
		{
			"oldest first",
			order{column: columnAge, descending: true},
			[]string{"acme/c#3", "acme/a#2", "acme/a#1", "acme/b#1"},
		},
		{"newest first", order{column: columnAge}, []string{"acme/b#1", "acme/a#1", "acme/a#2", "acme/c#3"}},
		{"review", order{column: columnReview}, []string{"acme/a#2", "acme/c#3", "acme/b#1", "acme/a#1"}},
		{"status", order{column: columnStatus}, []string{"acme/a#2", "acme/b#1", "acme/a#1", "acme/c#3"}},
		{"ci passing first", order{column: columnCI}, []string{"acme/a#1", "acme/a#2", "acme/c#3", "acme/b#1"}},
		{
			"ci failing first",
			order{column: columnCI, descending: true},
			[]string{"acme/b#1", "acme/c#3", "acme/a#2", "acme/a#1"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, pr := range sortPullRequests(prs, tt.order) {
				got = append(got, pr.Repository+"#"+strconv.Itoa(pr.Number))
			}
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

func TestSortPullRequests_DoesNotModifyInput(t *testing.T) {
	t.Parallel()
	prs := []ghclient.PullRequest{{Repository: "acme/b", Number: 1}, {Repository: "acme/a", Number: 1}}

	_ = sortPullRequests(prs, defaultOrder())

	assert.Equal(t, prs[0].Repository, "acme/b")
}

func TestSortPullRequests_CIFollowsTheBadgeShown(t *testing.T) {
	t.Parallel()
	prs := []ghclient.PullRequest{
		{Repository: "acme/a", Number: 1, CI: ghclient.CheckError},
		{Repository: "acme/a", Number: 2, CI: ghclient.CheckSuccess},
		{Repository: "acme/a", Number: 3, CI: ghclient.CheckFailure},
		{Repository: "acme/a", Number: 4, CI: ghclient.CheckExpected},
		{Repository: "acme/a", Number: 5, CI: ghclient.CheckPending},
	}

	got := sortPullRequests(prs, order{column: columnCI})

	numbers := make([]int, 0, len(got))
	for _, pr := range got {
		numbers = append(numbers, pr.Number)
	}
	// Failure and error look the same, as do pending and expected, so each pair stays in number order.
	assert.DeepEqual(t, numbers, []int{2, 4, 5, 1, 3})
}

// TestRanksCoverEveryBadge makes sure that a new badge cannot be added without deciding where it sorts.
func TestRanksCoverEveryBadge(t *testing.T) {
	t.Parallel()

	for _, decision := range []ghclient.ReviewDecision{
		"", ghclient.ReviewApproved, ghclient.ReviewChangesRequested, ghclient.ReviewRequired,
	} {
		assert.Assert(t, reviewRank(decision) < 4, "review decision %q is not ranked", decision)
	}
	for _, state := range []ghclient.MergeState{
		"", ghclient.MergeStateBehind, ghclient.MergeStateBlocked, ghclient.MergeStateClean, ghclient.MergeStateDirty,
		ghclient.MergeStateDraft, ghclient.MergeStateHasHooks, ghclient.MergeStateUnknown, ghclient.MergeStateUnstable,
	} {
		pr := ghclient.PullRequest{MergeState: state}
		assert.Assert(t, statusRank(pr) < 7, "merge state %q is not ranked", state)
	}
	assert.Assert(t, statusRank(ghclient.PullRequest{Draft: true}) < 7, "drafts are not ranked")
	for _, state := range []ghclient.CheckState{
		"", ghclient.CheckSuccess, ghclient.CheckFailure, ghclient.CheckError, ghclient.CheckPending, ghclient.CheckExpected,
	} {
		assert.Assert(t, ciRank(state) < 4, "check state %q is not ranked", state)
	}
}
