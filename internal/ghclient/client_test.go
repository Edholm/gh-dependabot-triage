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
// is not empty, each request first consumes the next error; a nil error lets the request succeed.
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
	next := f.responses[0]
	f.responses = f.responses[1:]
	return json.Unmarshal([]byte(next), response)
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	assert.NilError(t, err)
	return string(data)
}

func TestClient_ListReviewRequested(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{responses: []string{readFixture(t, "page1.json"), readFixture(t, "page2.json")}}

	got, err := ghclient.New(doer).ListReviewRequested(context.Background())

	assert.NilError(t, err)
	assert.Equal(t, len(got), 2, "non-dependabot and author-less pull requests must be dropped")

	queued := got[0]
	assert.Equal(t, queued.Repository, "acme/api")
	assert.Equal(t, queued.Number, 12)
	assert.Equal(t, queued.Author, "dependabot")
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
	assert.Equal(t, draft.CI, ghclient.CheckState(""), "a null status check rollup means no CI")
	assert.Equal(t, draft.ReviewDecision, ghclient.ReviewDecision(""))
	assert.Equal(t, draft.MergeState, ghclient.MergeStateUnknown)
	assert.Assert(t, !draft.AutoMerge)
	assert.Assert(t, draft.MergeQueue == nil)
}

func TestClient_ListReviewRequested_PaginatesAndFiltersByAuthor(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{responses: []string{readFixture(t, "page1.json"), readFixture(t, "page2.json")}}

	_, err := ghclient.New(doer).ListReviewRequested(context.Background())

	assert.NilError(t, err)
	assert.Equal(t, len(doer.calls), 2)
	for _, call := range doer.calls {
		query, ok := call["q"].(string)
		assert.Assert(t, ok)
		for _, qualifier := range []string{"is:pr", "is:open", "author:app/dependabot", "review-requested:@me"} {
			assert.Assert(t, strings.Contains(query, qualifier), "query %q is missing %q", query, qualifier)
		}
	}
	assert.Assert(t, doer.calls[0]["after"].(*string) == nil, "first page must not send a cursor")
	assert.Equal(t, *doer.calls[1]["after"].(*string), "cursor-1")
}

func TestClient_ListReviewRequested_Error(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{errs: []error{errors.New("boom")}}

	got, err := ghclient.New(doer).ListReviewRequested(context.Background())

	assert.ErrorContains(t, err, "search pull requests: boom")
	assert.Assert(t, got == nil)
}

func TestClient_ListReviewRequested_RetriesTransientErrors(t *testing.T) {
	t.Parallel()
	badGateway := &api.HTTPError{StatusCode: http.StatusBadGateway}
	doer := &fakeDoer{
		responses: []string{readFixture(t, "page2.json")},
		errs:      []error{badGateway, badGateway},
	}

	got, err := ghclient.New(doer, ghclient.WithRetryDelay(time.Millisecond)).ListReviewRequested(context.Background())

	assert.NilError(t, err)
	assert.Equal(t, len(got), 1)
	assert.Equal(t, len(doer.calls), 3)
}

func TestClient_ListReviewRequested_GivesUpOnPersistentTransientErrors(t *testing.T) {
	t.Parallel()
	badGateway := &api.HTTPError{StatusCode: http.StatusBadGateway, Message: "Bad Gateway"}
	doer := &fakeDoer{errs: []error{badGateway, badGateway, badGateway, badGateway}}

	_, err := ghclient.New(doer, ghclient.WithRetryDelay(time.Millisecond)).ListReviewRequested(context.Background())

	assert.ErrorContains(t, err, "search pull requests")
	assert.Equal(t, len(doer.calls), 3, "must stop after the maximum number of attempts")
}

func TestClient_ListReviewRequested_DoesNotRetryPermanentErrors(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{errs: []error{&api.HTTPError{StatusCode: http.StatusUnauthorized}}}

	_, err := ghclient.New(doer, ghclient.WithRetryDelay(time.Millisecond)).ListReviewRequested(context.Background())

	assert.ErrorContains(t, err, "search pull requests")
	assert.Equal(t, len(doer.calls), 1)
}

func TestClient_ListReviewRequested_StopsRetryingWhenContextIsCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	doer := &fakeDoer{errs: []error{&api.HTTPError{StatusCode: http.StatusBadGateway}}}

	_, err := ghclient.New(doer, ghclient.WithRetryDelay(time.Hour)).ListReviewRequested(ctx)

	assert.ErrorContains(t, err, "context canceled")
	assert.Equal(t, len(doer.calls), 1)
}
