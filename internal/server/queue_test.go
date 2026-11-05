package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
	"gotest.tools/v3/assert"
)

// fakeMerger answers status checks from a script per pull request and records every call. It is safe for concurrent
// use.
type fakeMerger struct {
	mu sync.Mutex
	// statuses are the answers to status checks, per pull request ID. The last one repeats.
	statuses map[string][]ghclient.Status
	// failing maps a call, as recorded in calls, to the error it fails with.
	failing map[string]error
	calls   []string
}

func (f *fakeMerger) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.failing[call]
}

func (f *fakeMerger) Status(_ context.Context, id string) (ghclient.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	script := f.statuses[id]
	if len(script) == 0 {
		return ghclient.Status{}, fmt.Errorf("no status for %s", id)
	}
	status := script[0]
	if len(script) > 1 {
		f.statuses[id] = script[1:]
	}
	return status, nil
}

func (f *fakeMerger) Approve(_ context.Context, id string) error {
	return f.record("approve " + id)
}

func (f *fakeMerger) UpdateBranch(_ context.Context, id, head string) error {
	return f.record("rebase " + id + " " + head)
}

func (f *fakeMerger) Merge(_ context.Context, id, head string) error {
	return f.record("merge " + id + " " + head)
}

func (f *fakeMerger) Enqueue(_ context.Context, id, head string) error {
	return f.record("enqueue " + id + " " + head)
}

func (f *fakeMerger) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func testPullRequest(repo string, number int) ghclient.PullRequest {
	return ghclient.PullRequest{
		ID:             fmt.Sprintf("PR_%d", number),
		Repository:     repo,
		Number:         number,
		Title:          fmt.Sprintf("bump %d", number),
		URL:            fmt.Sprintf("https://github.com/%s/pull/%d", repo, number),
		ReviewDecision: ghclient.ReviewRequired,
	}
}

func clean(head string) ghclient.Status {
	return ghclient.Status{
		State:      ghclient.PullRequestOpen,
		HeadOID:    head,
		MergeState: ghclient.MergeStateClean,
		CI:         ghclient.CheckSuccess,
	}
}

func withState(status ghclient.Status, mergeState ghclient.MergeState, ci ghclient.CheckState) ghclient.Status {
	status.MergeState, status.CI = mergeState, ci
	return status
}

// startQueue runs a queue backed by merger until the test ends. changed counts how often the queue reported a change.
func startQueue(t *testing.T, merger Merger) (*queue, *atomic.Int32) {
	t.Helper()
	var changed atomic.Int32
	q := newQueue(slog.New(slog.DiscardHandler), merger, func() { changed.Add(1) })
	q.pollInterval = time.Millisecond
	q.waitTimeout = waitTimeout
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		q.run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return q, &changed
}

// waitChanged waits until the queue has reported a change want times. The queue reports a change after it finished
// the work, so it can come a moment after waitIdle returns.
func waitChanged(t *testing.T, changed *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for changed.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d changes, got %d", want, changed.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

// waitIdle waits until q has finished all its work and returns its view.
func waitIdle(t *testing.T, q *queue) queueView {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		v := q.view()
		if !v.Busy {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the queue, it is at %+v", v)
		}
		time.Sleep(time.Millisecond)
	}
}

func steps(v queueView) map[string]string {
	got := make(map[string]string)
	for _, item := range v.Items {
		got[item.Key] = string(item.Step)
		if item.Message != "" {
			got[item.Key] += ": " + item.Message
		}
	}
	return got
}

func TestQueue_Merge_RebasesWhenBehindAndWaitsForCI(t *testing.T) {
	t.Parallel()
	merger := &fakeMerger{statuses: map[string][]ghclient.Status{
		"PR_1": {
			withState(clean("a"), ghclient.MergeStateBehind, ghclient.CheckSuccess),
			// GitHub has not caught up with the rebase yet: it must not be rebased twice.
			withState(clean("a"), ghclient.MergeStateBehind, ghclient.CheckSuccess),
			withState(clean("b"), ghclient.MergeStateUnknown, ""),
			withState(clean("b"), ghclient.MergeStateBlocked, ghclient.CheckPending),
			clean("b"),
		},
	}}
	q, changed := startQueue(t, merger)

	assert.NilError(t, q.add(context.Background(), actionMerge, []ghclient.PullRequest{testPullRequest("acme/api", 1)}))

	v := waitIdle(t, q)
	assert.DeepEqual(t, steps(v), map[string]string{"acme/api#1": "merged"})
	assert.DeepEqual(t, merger.recorded(), []string{"approve PR_1", "rebase PR_1 a", "merge PR_1 b"})
	// Once for the approval, once for the merge.
	waitChanged(t, changed, 2)
}

func TestQueue_Merge_ApprovesAllThenMergesInOrderPerRepository(t *testing.T) {
	t.Parallel()
	merger := &fakeMerger{statuses: map[string][]ghclient.Status{
		"PR_1": {clean("a")},
		"PR_2": {withState(clean("b"), ghclient.MergeStateBehind, ghclient.CheckSuccess), clean("c")},
		"PR_3": {clean("d")},
	}}
	q, _ := startQueue(t, merger)
	approved := testPullRequest("acme/api", 2)
	approved.ReviewDecision = ghclient.ReviewApproved

	assert.NilError(t, q.add(
		context.Background(),
		actionMerge,
		[]ghclient.PullRequest{testPullRequest("acme/api", 1), approved, testPullRequest("acme/web", 3)},
	))

	v := waitIdle(t, q)
	assert.DeepEqual(t, steps(v), map[string]string{
		"acme/api#1": "merged",
		"acme/api#2": "merged",
		"acme/web#3": "merged",
	})
	// Repositories run in parallel, so only the calls of one repository have a fixed order.
	var api []string
	for _, call := range merger.recorded() {
		if !strings.Contains(call, "PR_3") {
			api = append(api, call)
		}
	}
	assert.DeepEqual(t, api, []string{"approve PR_1", "merge PR_1 a", "rebase PR_2 b", "merge PR_2 c"})
}

func TestQueue_Merge_FailureMovesOnToTheNextPullRequest(t *testing.T) {
	t.Parallel()
	merger := &fakeMerger{statuses: map[string][]ghclient.Status{
		"PR_1": {withState(clean("a"), ghclient.MergeStateUnstable, ghclient.CheckFailure)},
		"PR_2": {withState(clean("b"), ghclient.MergeStateDirty, ghclient.CheckSuccess)},
		"PR_3": {{State: ghclient.PullRequestClosed}},
		"PR_4": {clean("d")},
	}}
	q, _ := startQueue(t, merger)

	prs := make([]ghclient.PullRequest, 0, 4)
	for n := 1; n <= 4; n++ {
		prs = append(prs, testPullRequest("acme/api", n))
	}
	assert.NilError(t, q.add(context.Background(), actionMerge, prs))

	assert.DeepEqual(t, steps(waitIdle(t, q)), map[string]string{
		"acme/api#1": "failed: CI failed",
		"acme/api#2": "failed: Conflicts with the base branch",
		"acme/api#3": "failed: Closed without being merged",
		"acme/api#4": "merged",
	})
}

func TestQueue_Merge_FailsWhenApprovalFails(t *testing.T) {
	t.Parallel()
	merger := &fakeMerger{
		statuses: map[string][]ghclient.Status{"PR_2": {clean("b")}},
		failing:  map[string]error{"approve PR_1": errors.New("forbidden")},
	}
	q, _ := startQueue(t, merger)

	assert.NilError(t, q.add(
		context.Background(),
		actionMerge,
		[]ghclient.PullRequest{testPullRequest("acme/api", 1), testPullRequest("acme/api", 2)},
	))

	assert.DeepEqual(t, steps(waitIdle(t, q)), map[string]string{
		"acme/api#1": "failed: forbidden",
		"acme/api#2": "merged",
	})
	assert.DeepEqual(t, merger.recorded(), []string{"approve PR_1", "approve PR_2", "merge PR_2 b"})
}

func TestQueue_Merge_ExplainsMissingWorkflowScope(t *testing.T) {
	t.Parallel()
	merger := &fakeMerger{
		statuses: map[string][]ghclient.Status{
			"PR_1": {withState(clean("a"), ghclient.MergeStateBehind, ghclient.CheckSuccess)},
		},
		failing: map[string]error{"rebase PR_1 a": fmt.Errorf("rebase: %w", ghclient.ErrMissingWorkflowScope)},
	}
	q, _ := startQueue(t, merger)

	assert.NilError(t, q.add(context.Background(), actionMerge, []ghclient.PullRequest{testPullRequest("acme/api", 1)}))

	got := steps(waitIdle(t, q))["acme/api#1"]
	assert.Assert(t, strings.Contains(got, "gh auth refresh -s workflow"), got)
}

func TestQueue_Merge_ExplainsWaitingOnReviewWhenEnqueuing(t *testing.T) {
	t.Parallel()
	status := clean("a")
	status.MergeQueueRequired = true
	merger := &fakeMerger{
		statuses: map[string][]ghclient.Status{"PR_1": {status}},
		failing: map[string]error{"enqueue PR_1 a": fmt.Errorf(
			"enqueue: %w", &ghclient.ReviewRequiredError{Reason: "code owner review from acme/platform"},
		)},
	}
	q, _ := startQueue(t, merger)

	assert.NilError(t, q.add(context.Background(), actionMerge, []ghclient.PullRequest{testPullRequest("acme/api", 1)}))

	assert.DeepEqual(t, steps(waitIdle(t, q)), map[string]string{
		"acme/api#1": "failed: Waiting on code owner review from acme/platform",
	})
}

func TestQueue_Merge_RetriesAFailedMergeOnce(t *testing.T) {
	t.Parallel()
	merger := &fakeMerger{
		statuses: map[string][]ghclient.Status{"PR_1": {clean("a")}},
		failing:  map[string]error{"merge PR_1 a": errors.New("base branch was modified")},
	}
	q, _ := startQueue(t, merger)

	assert.NilError(t, q.add(context.Background(), actionMerge, []ghclient.PullRequest{testPullRequest("acme/api", 1)}))

	assert.DeepEqual(t, steps(waitIdle(t, q)), map[string]string{"acme/api#1": "failed: base branch was modified"})
	assert.DeepEqual(t, merger.recorded(), []string{"approve PR_1", "merge PR_1 a", "merge PR_1 a"})
}

func TestQueue_Merge_UsesTheGitHubMergeQueueWhenRequired(t *testing.T) {
	t.Parallel()
	queued := withState(clean("a"), ghclient.MergeStateBlocked, ghclient.CheckSuccess)
	queued.MergeQueueRequired = true
	behind := withState(queued, ghclient.MergeStateBehind, ghclient.CheckSuccess)
	merger := &fakeMerger{statuses: map[string][]ghclient.Status{"PR_1": {behind}}}
	q, _ := startQueue(t, merger)

	assert.NilError(t, q.add(context.Background(), actionMerge, []ghclient.PullRequest{testPullRequest("acme/api", 1)}))

	assert.DeepEqual(t, steps(waitIdle(t, q)), map[string]string{"acme/api#1": "enqueued"})
	assert.DeepEqual(t, merger.recorded(), []string{"approve PR_1", "enqueue PR_1 a"})
}

func TestQueue_Merge_FailsWhenBlockedOnReview(t *testing.T) {
	t.Parallel()
	blocked := withState(clean("a"), ghclient.MergeStateBlocked, ghclient.CheckSuccess)
	blocked.ReviewDecision = ghclient.ReviewRequired
	merger := &fakeMerger{statuses: map[string][]ghclient.Status{"PR_1": {blocked}}}
	q, _ := startQueue(t, merger)

	assert.NilError(t, q.add(context.Background(), actionMerge, []ghclient.PullRequest{testPullRequest("acme/api", 1)}))

	assert.DeepEqual(t, steps(waitIdle(t, q)), map[string]string{"acme/api#1": "failed: Blocked: Review required"})
}

func TestQueue_Merge_GivesUpWaiting(t *testing.T) {
	t.Parallel()
	running := withState(clean("a"), ghclient.MergeStateBlocked, ghclient.CheckPending)
	merger := &fakeMerger{statuses: map[string][]ghclient.Status{"PR_1": {running}}}
	q, _ := startQueue(t, merger)
	q.waitTimeout = 20 * time.Millisecond

	assert.NilError(t, q.add(context.Background(), actionMerge, []ghclient.PullRequest{testPullRequest("acme/api", 1)}))

	v := waitIdle(t, q)
	assert.Equal(t, v.Items[0].Step, stepFailed)
	assert.Assert(t, strings.HasPrefix(v.Items[0].Message, "Gave up after waiting"), v.Items[0].Message)
}

func TestQueue_Approve(t *testing.T) {
	t.Parallel()
	merger := &fakeMerger{}
	q, changed := startQueue(t, merger)
	approved := testPullRequest("acme/api", 2)
	approved.ReviewDecision = ghclient.ReviewApproved

	assert.NilError(t, q.add(
		context.Background(),
		actionApprove,
		[]ghclient.PullRequest{testPullRequest("acme/api", 1), approved},
	))

	assert.DeepEqual(t, steps(waitIdle(t, q)), map[string]string{
		"acme/api#1": "approved",
		"acme/api#2": "approved: Already approved",
	})
	assert.DeepEqual(t, merger.recorded(), []string{"approve PR_1"})
	waitChanged(t, changed, 1)
	assert.Equal(t, q.view().Changes, 1, "the page must learn that it should refresh")
}

func TestQueue_SkipsPullRequestsItIsWorkingOn(t *testing.T) {
	t.Parallel()
	// Without a run loop nothing is worked on, so everything stays queued.
	q := newQueue(slog.New(slog.DiscardHandler), &fakeMerger{}, func() {})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pr := testPullRequest("acme/api", 1)

	_ = q.add(ctx, actionMerge, []ghclient.PullRequest{pr})
	_ = q.add(ctx, actionApprove, []ghclient.PullRequest{pr})

	assert.Equal(t, len(q.view().Items), 1)
}

func TestQueue_Remove_StopsWaitingForCI(t *testing.T) {
	t.Parallel()
	pr1, pr2 := testPullRequest("acme/api", 1), testPullRequest("acme/api", 2)
	merger := &fakeMerger{statuses: map[string][]ghclient.Status{
		"PR_1": {withState(clean("a"), ghclient.MergeStateBlocked, ghclient.CheckPending)},
		"PR_2": {clean("b")},
	}}
	q, _ := startQueue(t, merger)

	assert.NilError(t, q.add(t.Context(), actionMerge, []ghclient.PullRequest{pr1, pr2}))
	deadline := time.Now().Add(5 * time.Second)
	for steps(q.view())["acme/api#1"] != "ci" {
		assert.Assert(t, time.Now().Before(deadline), "never waited for CI")
		time.Sleep(time.Millisecond)
	}
	assert.Assert(t, q.remove("acme/api#1"))

	v := waitIdle(t, q)
	assert.DeepEqual(t, steps(v), map[string]string{"acme/api#2": "merged"})
	for _, call := range merger.recorded() {
		assert.Assert(t, call != "merge PR_1 a", "a removed pull request must not be merged")
	}
}

func TestQueue_Remove_StopsWaitingForABlockedPullRequest(t *testing.T) {
	t.Parallel()
	pr := testPullRequest("acme/api", 1)
	blocked := withState(clean("a"), ghclient.MergeStateBlocked, ghclient.CheckSuccess)
	blocked.ReviewDecision = ghclient.ReviewApproved
	merger := &fakeMerger{statuses: map[string][]ghclient.Status{"PR_1": {blocked}}}
	q, _ := startQueue(t, merger)

	assert.NilError(t, q.add(t.Context(), actionMerge, []ghclient.PullRequest{pr}))
	deadline := time.Now().Add(5 * time.Second)
	for steps(q.view())["acme/api#1"] != "waiting: Blocked by branch protection" {
		assert.Assert(t, time.Now().Before(deadline), "never waited, at %v", steps(q.view()))
		time.Sleep(time.Millisecond)
	}
	assert.Assert(t, q.remove("acme/api#1"))

	assert.DeepEqual(t, waitIdle(t, q).Items, []queueItemView{})
}

func TestQueue_RemoveAndClear(t *testing.T) {
	t.Parallel()
	q := newQueue(slog.New(slog.DiscardHandler), &fakeMerger{}, func() {})
	q.items = []*queueItem{
		{key: "acme/api#1", step: stepQueued},
		{key: "acme/api#2", step: stepCI},
		{key: "acme/api#3", step: stepMerged},
		{key: "acme/api#4", step: stepFailed},
	}

	assert.Assert(t, q.remove("acme/api#1"))
	assert.Assert(t, q.remove("acme/api#2"), "a pull request the queue is only waiting for can be removed")
	q.items = append(q.items, &queueItem{key: "acme/api#5", step: stepMerging})
	assert.Assert(t, !q.remove("acme/api#5"), "a pull request the queue is merging must stay")
	q.clearFinished()

	assert.DeepEqual(t, steps(q.view()), map[string]string{"acme/api#5": "merging"})
}

// postJSON posts body to target the way the page does, with headers adjusted by modify.
func postJSON(
	t *testing.T,
	handler http.Handler,
	target, body string,
	modify func(*http.Request),
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, target, strings.NewReader(body))
	req.Host = "localhost:8080"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:8080")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	if modify != nil {
		modify(req)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestServer_QueueEndpoints_RejectCrossOriginRequests(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t, newTestServer(t, &fakeLister{}))
	body := `{"action":"merge","pullRequests":["acme/api#1"]}`

	for _, tt := range []struct {
		name   string
		modify func(*http.Request)
		want   int
	}{
		{
			name:   "form post",
			modify: func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
			want:   http.StatusUnsupportedMediaType,
		},
		{
			name:   "text post",
			modify: func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
			want:   http.StatusUnsupportedMediaType,
		},
		{
			name:   "other origin",
			modify: func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") },
			want:   http.StatusForbidden,
		},
		{
			name:   "cross site",
			modify: func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
			want:   http.StatusForbidden,
		},
	} {
		for _, target := range []string{"/queue", "/queue/remove", "/queue/clear"} {
			rec := postJSON(t, handler, target, body, tt.modify)
			assert.Equal(t, rec.Code, tt.want, "%s to %s", tt.name, target)
		}
	}
}

func TestServer_QueueEndpoints(t *testing.T) {
	t.Parallel()
	lister := &fakeLister{prs: []ghclient.PullRequest{testPullRequest("acme/api", 1)}}
	srv := newTestServer(t, lister)
	assert.NilError(t, srv.Refresh(context.Background()))
	handler := newTestHandler(t, srv)

	rec := postJSON(t, handler, "/queue", `{"action":"delete","pullRequests":["acme/api#1"]}`, nil)
	assert.Equal(t, rec.Code, http.StatusBadRequest)

	rec = postJSON(t, handler, "/queue", `{"action":"merge","pullRequests":["acme/api#2"]}`, nil)
	assert.Equal(t, rec.Code, http.StatusUnprocessableEntity, "unknown pull requests must be rejected")

	// The server is not running, so the request times out handing the work over, but the item is queued.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	req := httptest.NewRequestWithContext(
		ctx,
		http.MethodPost,
		"/queue",
		strings.NewReader(`{"action":"merge","pullRequests":["acme/api#1"]}`),
	)
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	rec = do(t, handler, http.MethodGet, "/queue", "localhost")
	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Assert(t, strings.Contains(rec.Body.String(), `"key":"acme/api#1"`), rec.Body.String())
	assert.Assert(t, strings.Contains(rec.Body.String(), `"busy":true`), rec.Body.String())

	rec = postJSON(t, handler, "/queue/remove", `{"key":"acme/api#1"}`, nil)
	assert.Equal(t, rec.Code, http.StatusOK)
	assert.Assert(t, strings.Contains(rec.Body.String(), `"items":[]`), rec.Body.String())

	rec = postJSON(t, handler, "/queue/remove", `{"key":"acme/api#1"}`, nil)
	assert.Equal(t, rec.Code, http.StatusConflict)
}

func TestServer_Index_RendersSelection(t *testing.T) {
	t.Parallel()
	passing := testPullRequest("acme/api", 1)
	passing.CI = ghclient.CheckSuccess
	passing.MergeState = ghclient.MergeStateBehind
	failing := testPullRequest("acme/api", 2)
	failing.CI = ghclient.CheckFailure
	conflicting := testPullRequest("acme/api", 3)
	conflicting.CI = ghclient.CheckSuccess
	conflicting.MergeState = ghclient.MergeStateDirty
	srv := newTestServer(t, &fakeLister{prs: []ghclient.PullRequest{passing, failing, conflicting}})
	assert.NilError(t, srv.Refresh(context.Background()))

	body := index(t, newTestHandler(t, srv))

	assert.Assert(
		t,
		strings.Contains(body, `data-key="acme/api#1" data-url="`+passing.URL+`" data-ci="good" data-passing>`),
		body,
	)
	assert.Assert(t, strings.Contains(body, `data-key="acme/api#2" data-url="`+failing.URL+`" data-ci="bad">`), body)
	assert.Assert(
		t,
		strings.Contains(body, `data-key="acme/api#3" data-url="`+conflicting.URL+`" data-ci="good">`),
		body,
	)
	assert.Assert(t, strings.Contains(body, `value="acme/api#1"`), body)
	assert.Assert(t, strings.Contains(body, `data-select="passing"`), body)
	assert.Assert(t, strings.Contains(body, `data-select="failing"`), body)
	assert.Assert(t, strings.Contains(body, `class="prompt-dialog"`), body)
	assert.Assert(t, strings.Contains(body, `class="island js-only"`), body)
}
