package ghclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
	"github.com/cli/go-gh/v2/pkg/api"
	"gotest.tools/v3/assert"
)

// fakeDoer answers each request with the next canned response and records the variables it was called with. If errs
// is not empty, each request first consumes the next error; a nil error lets the request succeed. Once the responses
// run out, it answers with an empty search result.
type fakeDoer struct {
	responses []string
	errs      []error
	calls     []map[string]any
}

func (f *fakeDoer) DoWithContext(_ context.Context, _ string, variables map[string]any, response any) error {
	f.calls = append(f.calls, variables)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return err
		}
	}
	next := `{"search": {"pageInfo": {"hasNextPage": false}, "nodes": []}}`
	if len(f.responses) > 0 {
		next = f.responses[0]
		f.responses = f.responses[1:]
	}
	return json.Unmarshal([]byte(next), response)
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	assert.NilError(t, err)
	return string(data)
}

func TestClient_ListPullRequests(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{responses: []string{readFixture(t, "page1.json"), readFixture(t, "page2.json")}}

	got, err := ghclient.New(doer).ListPullRequests(context.Background())

	assert.NilError(t, err)
	assert.Equal(t, len(got), 2, "non-dependabot and author-less pull requests must be dropped")

	queued := got[0]
	assert.Equal(t, queued.Repository, "acme/api")
	assert.Equal(t, queued.Number, 12)
	assert.Equal(t, queued.Author, "dependabot")
	assert.Equal(t, queued.Description, "Bumps example.com/lib from 1.0.0 to 1.1.0.\n\nRelease notes\nFixes a bug.")
	assert.Equal(t, queued.ReviewDecision, ghclient.ReviewRequired)
	assert.Equal(t, queued.MergeState, ghclient.MergeStateBlocked)
	assert.Equal(t, queued.CI, ghclient.CheckSuccess)
	assert.Assert(t, queued.AutoMerge)
	assert.Assert(t, queued.MergeQueue != nil)
	assert.Equal(t, queued.MergeQueue.Position, 2)
	assert.Assert(t, queued.CreatedAt.Equal(time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)))

	draft := got[1]
	assert.Equal(t, draft.Repository, "acme/web")
	assert.Assert(t, draft.Draft)
	assert.Equal(t, draft.Description, "", "a pull request without a body has no description")
	assert.Equal(t, draft.CI, ghclient.CheckState(""), "a null status check rollup means no CI")
	assert.Equal(t, draft.ReviewDecision, ghclient.ReviewDecision(""))
	assert.Equal(t, draft.MergeState, ghclient.MergeStateUnknown)
	assert.Assert(t, !draft.AutoMerge)
	assert.Assert(t, draft.MergeQueue == nil)
}

func TestClient_ListPullRequests_PaginatesAndFiltersByAuthor(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{responses: []string{readFixture(t, "page1.json"), readFixture(t, "page2.json")}}

	_, err := ghclient.New(doer).ListPullRequests(context.Background())

	assert.NilError(t, err)
	assert.Equal(t, len(doer.calls), 3, "two pages of review requests, then one of reviewed pull requests")
	for i, call := range doer.calls {
		query, ok := call["q"].(string)
		assert.Assert(t, ok)
		reviewer := "review-requested:@me"
		if i == 2 {
			reviewer = "reviewed-by:@me"
		}
		for _, qualifier := range []string{"is:pr", "is:open", "author:app/dependabot", reviewer} {
			assert.Assert(t, strings.Contains(query, qualifier), "query %q is missing %q", query, qualifier)
		}
	}
	assert.Assert(t, doer.calls[0]["after"].(*string) == nil, "first page must not send a cursor")
	assert.Equal(t, *doer.calls[1]["after"].(*string), "cursor-1")
	assert.Assert(t, doer.calls[2]["after"].(*string) == nil, "a new search must start without a cursor")
}

func TestClient_ListPullRequests_IncludesReviewedPullRequestsOnce(t *testing.T) {
	t.Parallel()
	// The pull request of page 2 is both requested and reviewed, the one of page 1 only reviewed.
	doer := &fakeDoer{responses: []string{
		readFixture(t, "page2.json"),
		strings.Replace(readFixture(t, "page1.json"), `"hasNextPage": true`, `"hasNextPage": false`, 1),
		readFixture(t, "page2.json"),
	}}

	got, err := ghclient.New(doer).ListPullRequests(context.Background())

	assert.NilError(t, err)
	assert.Equal(t, len(got), 2)
	assert.Equal(t, got[0].Repository, "acme/web")
	assert.Equal(t, got[1].Repository, "acme/api")
}

func TestClient_ListPullRequests_Error(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{errs: []error{errors.New("boom")}}

	got, err := ghclient.New(doer).ListPullRequests(context.Background())

	assert.ErrorContains(t, err, "search pull requests: boom")
	assert.Assert(t, got == nil)
}

func TestClient_ListPullRequests_RetriesTransientErrors(t *testing.T) {
	t.Parallel()
	badGateway := &api.HTTPError{StatusCode: http.StatusBadGateway}
	doer := &fakeDoer{
		responses: []string{readFixture(t, "page2.json")},
		errs:      []error{badGateway, badGateway},
	}

	got, err := ghclient.New(doer, ghclient.WithRetryDelay(time.Millisecond)).ListPullRequests(context.Background())

	assert.NilError(t, err)
	assert.Equal(t, len(got), 1)
	assert.Equal(t, len(doer.calls), 4, "three attempts at the first search, then the second search")
}

func TestClient_ListPullRequests_GivesUpOnPersistentTransientErrors(t *testing.T) {
	t.Parallel()
	badGateway := &api.HTTPError{StatusCode: http.StatusBadGateway, Message: "Bad Gateway"}
	doer := &fakeDoer{errs: []error{badGateway, badGateway, badGateway, badGateway}}

	_, err := ghclient.New(doer, ghclient.WithRetryDelay(time.Millisecond)).ListPullRequests(context.Background())

	assert.ErrorContains(t, err, "search pull requests")
	assert.Equal(t, len(doer.calls), 3, "must stop after the maximum number of attempts")
}

func TestClient_ListPullRequests_DoesNotRetryPermanentErrors(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{errs: []error{&api.HTTPError{StatusCode: http.StatusUnauthorized}}}

	_, err := ghclient.New(doer, ghclient.WithRetryDelay(time.Millisecond)).ListPullRequests(context.Background())

	assert.ErrorContains(t, err, "search pull requests")
	assert.Equal(t, len(doer.calls), 1)
}

func TestClient_ListPullRequests_StopsRetryingWhenContextIsCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	doer := &fakeDoer{errs: []error{&api.HTTPError{StatusCode: http.StatusBadGateway}}}

	_, err := ghclient.New(doer, ghclient.WithRetryDelay(time.Hour)).ListPullRequests(ctx)

	assert.ErrorContains(t, err, "context canceled")
	assert.Equal(t, len(doer.calls), 1)
}

func TestClient_Status(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{responses: []string{`{"node": {
		"state": "OPEN",
		"headRefOid": "abc123",
		"reviewDecision": "APPROVED",
		"mergeStateStatus": "BEHIND",
		"isMergeQueueEnabled": true,
		"mergeQueueEntry": {"state": "QUEUED"},
		"commits": {"nodes": [{"commit": {"statusCheckRollup": {"state": "PENDING"}}}]}
	}}`}}

	got, err := ghclient.New(doer).Status(context.Background(), "PR_1")

	assert.NilError(t, err)
	assert.DeepEqual(t, got, ghclient.Status{
		State:              ghclient.PullRequestOpen,
		HeadOID:            "abc123",
		ReviewDecision:     ghclient.ReviewApproved,
		MergeState:         ghclient.MergeStateBehind,
		CI:                 ghclient.CheckPending,
		MergeQueueRequired: true,
		InMergeQueue:       true,
	})
	assert.DeepEqual(t, doer.calls, []map[string]any{{"id": "PR_1"}})
}

func TestClient_Status_NotFound(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{responses: []string{`{"node": null}`}}

	_, err := ghclient.New(doer).Status(context.Background(), "PR_1")

	assert.ErrorContains(t, err, "not found")
}

func TestClient_Mutations(t *testing.T) {
	t.Parallel()
	client := func(doer *fakeDoer) *ghclient.Client { return ghclient.New(doer, ghclient.WithRetryDelay(0)) }
	for _, tt := range []struct {
		name string
		call func(*ghclient.Client) error
		want map[string]any
	}{
		{
			name: "approve",
			call: func(c *ghclient.Client) error { return c.Approve(context.Background(), "PR_1") },
			want: map[string]any{"id": "PR_1"},
		},
		{
			name: "update branch",
			call: func(c *ghclient.Client) error { return c.UpdateBranch(context.Background(), "PR_1", "abc") },
			want: map[string]any{"id": "PR_1", "head": "abc"},
		},
		{
			name: "merge",
			call: func(c *ghclient.Client) error { return c.Merge(context.Background(), "PR_1", "abc") },
			want: map[string]any{"id": "PR_1", "head": "abc"},
		},
		{
			name: "enqueue",
			call: func(c *ghclient.Client) error { return c.Enqueue(context.Background(), "PR_1", "abc") },
			want: map[string]any{"id": "PR_1", "head": "abc"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doer := &fakeDoer{responses: []string{`{}`}}
			assert.NilError(t, tt.call(client(doer)))
			assert.DeepEqual(t, doer.calls, []map[string]any{tt.want})

			// Mutations are never retried, not even on transient errors.
			failing := &fakeDoer{errs: []error{&api.HTTPError{StatusCode: http.StatusBadGateway}}}
			assert.Assert(t, tt.call(client(failing)) != nil)
			assert.Equal(t, len(failing.calls), 1)
		})
	}
}
