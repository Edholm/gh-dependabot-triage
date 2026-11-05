package server

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
)

const (
	// DefaultPollInterval is how often the merge queue checks a pull request it is waiting for.
	DefaultPollInterval = 15 * time.Second
	// DefaultWaitTimeout is how long the merge queue waits for a pull request to become mergeable, for example for its
	// CI to finish, before it gives up on it.
	DefaultWaitTimeout = 30 * time.Minute

	// maxRebases bounds how often one pull request is rebased. Busy repositories could otherwise keep a pull request
	// behind forever.
	maxRebases = 5
	// maxStatusErrors is how many status checks in a row may fail before the queue gives up on a pull request.
	maxStatusErrors = 3
	// maxMergeErrors is how many merge attempts may fail before the queue gives up on a pull request. A merge can fail
	// because the base branch moved after the last status check; the next check then sees the pull request as behind.
	maxMergeErrors = 2
)

// Merger approves and merges pull requests on GitHub. The IDs are GraphQL node IDs, heads are commit OIDs.
type Merger interface {
	Status(ctx context.Context, id string) (ghclient.Status, error)
	Approve(ctx context.Context, id string) error
	UpdateBranch(ctx context.Context, id, head string) error
	Merge(ctx context.Context, id, head string) error
	Enqueue(ctx context.Context, id, head string) error
}

// action is what the user asked the queue to do with a pull request.
type action string

const (
	actionApprove action = "approve"
	actionMerge   action = "merge"
)

// step is where a pull request is in the queue.
type step string

const (
	stepQueued    step = "queued"
	stepApproving step = "approving"
	stepWaiting   step = "waiting"
	stepRebasing  step = "rebasing"
	stepCI        step = "ci"
	stepMerging   step = "merging"
	// The steps below are final.
	stepApproved step = "approved"
	stepMerged   step = "merged"
	stepEnqueued step = "enqueued"
	stepFailed   step = "failed"
)

// finished reports whether the queue is done with a pull request in step s.
func (s step) finished() bool {
	switch s {
	case stepApproved, stepMerged, stepEnqueued, stepFailed:
		return true
	default:
		return false
	}
}

// badge returns how step s is shown.
func (s step) badge() badge {
	switch s {
	case stepQueued:
		return badge{Label: "Queued", Class: classNeutral}
	case stepApproving:
		return badge{Label: "Approving…", Class: classInfo}
	case stepWaiting:
		return badge{Label: "Waiting", Class: classWarn}
	case stepRebasing:
		return badge{Label: "Rebasing…", Class: classInfo}
	case stepCI:
		return badge{Label: "Waiting for CI", Class: classWarn}
	case stepMerging:
		return badge{Label: "Merging…", Class: classInfo}
	case stepApproved:
		return badge{Label: "Approved", Class: classGood}
	case stepMerged:
		return badge{Label: "Merged", Class: classGood}
	case stepEnqueued:
		return badge{Label: "In merge queue", Class: classGood}
	case stepFailed:
		return badge{Label: "Failed", Class: classBad}
	default:
		return badge{Label: string(s), Class: classNeutral}
	}
}

// queueItem is a pull request the user asked the queue to approve or merge. It is guarded by queue.mu.
type queueItem struct {
	key    string
	pr     ghclient.PullRequest
	action action
	step   step
	// message explains the step, for example why the pull request failed.
	message string
	// approved is set once the pull request has been approved, or was approved already.
	approved bool
}

// queue approves pull requests and merges them one at a time per repository: most repositories require pull
// requests to be up to date with their base branch, so every merge makes the next pull request of the same
// repository fall behind. Pull requests of different repositories are merged in parallel.
type queue struct {
	logger       *slog.Logger
	merger       Merger
	pollInterval time.Duration
	waitTimeout  time.Duration
	// changed is called whenever a pull request has been approved or merged, so that the table can be refreshed.
	changed func()
	// tasks hands work to the goroutine running run, which owns the lifetime of all work.
	tasks chan func(context.Context)

	mu    sync.Mutex
	items []*queueItem
	// merging holds the repositories that have a merge worker running.
	merging map[string]bool
	// changes counts the approvals and merges. The page refreshes the table whenever it grows.
	changes int
}

func newQueue(logger *slog.Logger, merger Merger, changed func()) *queue {
	return &queue{
		logger:       logger,
		merger:       merger,
		pollInterval: DefaultPollInterval,
		waitTimeout:  DefaultWaitTimeout,
		changed:      changed,
		tasks:        make(chan func(context.Context)),
		merging:      make(map[string]bool),
	}
}

// run runs the work handed to the queue until ctx is canceled, then waits for that work to stop.
func (q *queue) run(ctx context.Context) {
	var workers sync.WaitGroup
	defer workers.Wait()
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-q.tasks:
			workers.Go(func() { task(ctx) })
		}
	}
}

// add queues prs for a. Pull requests the queue is already working on are skipped. It returns once the work has been
// handed to run, or when ctx is canceled.
func (q *queue) add(ctx context.Context, a action, prs []ghclient.PullRequest) error {
	var added []*queueItem
	var repos []string
	q.mu.Lock()
	for _, pr := range prs {
		key := pullRequestKey(pr)
		if q.busyWith(key) {
			continue
		}
		item := &queueItem{key: key, pr: pr, action: a, step: stepQueued}
		q.items = append(q.items, item)
		added = append(added, item)
		if a == actionMerge && !q.merging[pr.Repository] {
			q.merging[pr.Repository] = true
			repos = append(repos, pr.Repository)
		}
	}
	q.mu.Unlock()

	var tasks []func(context.Context)
	switch a {
	case actionApprove:
		if len(added) > 0 {
			tasks = append(tasks, func(ctx context.Context) { q.approveAll(ctx, added) })
		}
	case actionMerge:
		for _, repo := range repos {
			tasks = append(tasks, func(ctx context.Context) { q.mergeAll(ctx, repo) })
		}
	}
	for _, task := range tasks {
		select {
		case q.tasks <- task:
		case <-ctx.Done():
			return fmt.Errorf("queue pull requests: %v", ctx.Err())
		}
	}
	return nil
}

// busyWith reports whether the queue has unfinished work for the pull request with key. q.mu must be held.
func (q *queue) busyWith(key string) bool {
	return slices.ContainsFunc(q.items, func(item *queueItem) bool { return item.key == key && !item.step.finished() })
}

// remove drops the pull request with key from the queue, unless the queue has started working on it. It reports
// whether the pull request was removed.
func (q *queue) remove(key string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := slices.IndexFunc(q.items, func(item *queueItem) bool { return item.key == key && item.step == stepQueued })
	if i < 0 {
		return false
	}
	q.items = slices.Delete(q.items, i, i+1)
	return true
}

// clearFinished drops all pull requests the queue is done with.
func (q *queue) clearFinished() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = slices.DeleteFunc(q.items, func(item *queueItem) bool { return item.step.finished() })
}

// notifyChanged reports that pull requests were approved or merged, and then records it. In that order, a page that
// sees the change also sees that the server is fetching the new state of the pull requests.
func (q *queue) notifyChanged() {
	q.changed()
	q.mu.Lock()
	q.changes++
	q.mu.Unlock()
}

// set moves item to step s with message.
func (q *queue) set(item *queueItem, s step, message string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	item.step, item.message = s, message
}

// approveAll approves items one after another.
func (q *queue) approveAll(ctx context.Context, items []*queueItem) {
	for _, item := range items {
		q.mu.Lock()
		if item.step != stepQueued {
			// Removed from the queue in the meantime.
			q.mu.Unlock()
			continue
		}
		item.step = stepApproving
		q.mu.Unlock()
		if !q.approve(ctx, item) {
			continue
		}
		q.mu.Lock()
		item.step = stepApproved
		q.mu.Unlock()
	}
	q.notifyChanged()
}

// approve approves item unless it is approved already. It reports whether item is approved, and fails item if it
// could not be approved.
func (q *queue) approve(ctx context.Context, item *queueItem) bool {
	if item.pr.ReviewDecision == ghclient.ReviewApproved {
		q.mu.Lock()
		item.approved, item.message = true, "Already approved"
		q.mu.Unlock()
		return true
	}
	if err := q.merger.Approve(ctx, item.pr.ID); err != nil {
		q.logger.Error("approve failed", "pull_request", item.key, "error", err)
		q.set(item, stepFailed, err.Error())
		return false
	}
	q.mu.Lock()
	item.approved, item.message = true, ""
	q.mu.Unlock()
	return true
}

// mergeAll approves and then merges the pull requests queued for merging in repo, in the order they were queued,
// until there are none left.
func (q *queue) mergeAll(ctx context.Context, repo string) {
	for ctx.Err() == nil {
		// Approve everything queued so far up front, so that reviewers see it right away.
		approved := false
		for item := q.nextToApprove(repo); item != nil; item = q.nextToApprove(repo) {
			if q.approve(ctx, item) {
				q.set(item, stepQueued, "")
				approved = true
			}
		}
		if approved {
			q.notifyChanged()
		}
		item := q.nextToMerge(repo)
		if item == nil {
			return
		}
		q.merge(ctx, item)
		q.notifyChanged()
	}
}

// nextToApprove returns the first item queued for merging in repo that has not been approved, and moves it to
// stepApproving. It returns nil if there is none.
func (q *queue) nextToApprove(repo string) *queueItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, item := range q.items {
		if item.pr.Repository == repo && item.action == actionMerge && item.step == stepQueued && !item.approved {
			item.step = stepApproving
			return item
		}
	}
	return nil
}

// nextToMerge returns the first item queued for merging in repo, and moves it to stepWaiting. If there is none, it
// marks repo as no longer merging and returns nil, so that the caller can stop.
func (q *queue) nextToMerge(repo string) *queueItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, item := range q.items {
		if item.pr.Repository == repo && item.action == actionMerge && item.step == stepQueued {
			item.step = stepWaiting
			return item
		}
	}
	delete(q.merging, repo)
	return nil
}

// merge waits for item to become mergeable, rebasing it onto its base branch whenever it falls behind, and merges it.
// The outcome is recorded on item.
func (q *queue) merge(ctx context.Context, item *queueItem) {
	logger := q.logger.With("pull_request", item.key)
	m := merging{deadline: time.Now().Add(q.waitTimeout)}
	for {
		status, err := q.merger.Status(ctx, item.pr.ID)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			m.statusErrors++
			logger.Warn("status check failed", "error", err)
			if m.statusErrors >= maxStatusErrors {
				q.set(item, stepFailed, err.Error())
				return
			}
		default:
			m.statusErrors = 0
			if q.advance(ctx, logger, item, status, &m) {
				return
			}
		}
		if time.Now().After(m.deadline) {
			q.mu.Lock()
			message := fmt.Sprintf("Gave up after waiting %s: %s", q.waitTimeout, item.message)
			item.step, item.message = stepFailed, message
			q.mu.Unlock()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(q.pollInterval):
		}
	}
}

// merging is the progress of merging one pull request.
type merging struct {
	// deadline is when to give up waiting. It moves on every rebase, since a rebase restarts CI.
	deadline time.Time
	// rebasedHead is the head commit the pull request was last rebased from. Until GitHub has caught up with the
	// rebase, the pull request still looks behind at that commit.
	rebasedHead  string
	rebases      int
	statusErrors int
	mergeErrors  int
}

// advance takes the next step towards merging item, given its current status. It reports whether item is finished.
func (q *queue) advance(
	ctx context.Context,
	logger *slog.Logger,
	item *queueItem,
	status ghclient.Status,
	m *merging,
) bool {
	ciFailed := status.CI == ghclient.CheckFailure || status.CI == ghclient.CheckError
	ciRunning := status.CI == ghclient.CheckPending || status.CI == ghclient.CheckExpected
	switch {
	case status.State == ghclient.PullRequestMerged:
		q.set(item, stepMerged, "")
		return true
	case status.State == ghclient.PullRequestClosed:
		q.set(item, stepFailed, "Closed without being merged")
		return true
	case status.InMergeQueue:
		q.set(item, stepEnqueued, "")
		return true
	case status.MergeState == ghclient.MergeStateDirty:
		q.set(item, stepFailed, "Conflicts with the base branch")
		return true
	case status.MergeState == ghclient.MergeStateBehind && !status.MergeQueueRequired:
		return q.rebase(ctx, logger, item, status, m)
	case ciFailed:
		q.set(item, stepFailed, "CI failed")
		return true
	case ciRunning:
		q.set(item, stepCI, "")
		return false
	case status.MergeQueueRequired:
		if err := q.merger.Enqueue(ctx, item.pr.ID, status.HeadOID); err != nil {
			logger.Error("enqueue failed", "error", err)
			q.set(item, stepFailed, err.Error())
			return true
		}
		q.set(item, stepEnqueued, "")
		return true
	case status.MergeState == ghclient.MergeStateBlocked:
		switch status.ReviewDecision {
		case ghclient.ReviewRequired, ghclient.ReviewChangesRequested:
			q.set(item, stepFailed, "Blocked: "+reviewBadge(status.ReviewDecision).Label)
			return true
		default:
			q.set(item, stepWaiting, "Blocked by branch protection")
			return false
		}
	case status.MergeState == ghclient.MergeStateClean ||
		status.MergeState == ghclient.MergeStateHasHooks ||
		status.MergeState == ghclient.MergeStateUnstable:
		q.set(item, stepMerging, "")
		if err := q.merger.Merge(ctx, item.pr.ID, status.HeadOID); err != nil {
			m.mergeErrors++
			logger.Warn("merge failed", "error", err)
			if m.mergeErrors >= maxMergeErrors {
				q.set(item, stepFailed, err.Error())
				return true
			}
			q.set(item, stepWaiting, "Merge failed, retrying")
			return false
		}
		logger.Info("merged")
		q.set(item, stepMerged, "")
		return true
	default:
		q.set(item, stepWaiting, "GitHub is checking mergeability")
		return false
	}
}

// rebase rebases item onto its base branch, unless it was already rebased from its current head. It reports whether
// item is finished, which it only is when the rebase failed.
func (q *queue) rebase(
	ctx context.Context,
	logger *slog.Logger,
	item *queueItem,
	status ghclient.Status,
	m *merging,
) bool {
	if status.HeadOID == m.rebasedHead {
		// GitHub has not caught up with the rebase yet.
		return false
	}
	if m.rebases >= maxRebases {
		q.set(item, stepFailed, fmt.Sprintf("Still behind the base branch after %d rebases", maxRebases))
		return true
	}
	q.set(item, stepRebasing, "")
	if err := q.merger.UpdateBranch(ctx, item.pr.ID, status.HeadOID); err != nil {
		logger.Error("rebase failed", "error", err)
		q.set(item, stepFailed, err.Error())
		return true
	}
	logger.Info("rebased onto the base branch")
	m.rebasedHead = status.HeadOID
	m.rebases++
	m.deadline = time.Now().Add(q.waitTimeout)
	return false
}

// queueView is the state of the queue as the page shows it.
type queueView struct {
	Items []queueItemView `json:"items"`
	// Busy reports whether the queue has unfinished work.
	Busy bool `json:"busy"`
	// Changes counts the approvals and merges, see queue.changes.
	Changes int `json:"changes"`
}

type queueItemView struct {
	Key        string `json:"key"`
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Action     action `json:"action"`
	Step       step   `json:"step"`
	Label      string `json:"label"`
	Class      string `json:"class"`
	Message    string `json:"message"`
	Finished   bool   `json:"finished"`
	// Removable reports whether the pull request can still be taken out of the queue.
	Removable bool `json:"removable"`
}

// view returns the current state of the queue.
func (q *queue) view() queueView {
	q.mu.Lock()
	defer q.mu.Unlock()
	v := queueView{Items: make([]queueItemView, 0, len(q.items)), Changes: q.changes}
	for _, item := range q.items {
		b := item.step.badge()
		v.Items = append(v.Items, queueItemView{
			Key:        item.key,
			Repository: item.pr.Repository,
			Number:     item.pr.Number,
			Title:      item.pr.Title,
			URL:        item.pr.URL,
			Action:     item.action,
			Step:       item.step,
			Label:      b.Label,
			Class:      b.Class,
			Message:    item.message,
			Finished:   item.step.finished(),
			Removable:  item.step == stepQueued,
		})
		if !item.step.finished() {
			v.Busy = true
		}
	}
	return v
}

// pullRequestKey identifies pr on the page, in owner/name#number form.
func pullRequestKey(pr ghclient.PullRequest) string {
	return fmt.Sprintf("%s#%d", pr.Repository, pr.Number)
}
