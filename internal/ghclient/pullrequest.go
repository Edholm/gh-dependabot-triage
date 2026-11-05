package ghclient

import "time"

// ReviewDecision is the GitHub review decision of a pull request. It is empty when no review is required.
type ReviewDecision string

// Review decisions reported by GitHub.
const (
	ReviewApproved         ReviewDecision = "APPROVED"
	ReviewChangesRequested ReviewDecision = "CHANGES_REQUESTED"
	ReviewRequired         ReviewDecision = "REVIEW_REQUIRED"
)

// MergeState is the GitHub merge state status of a pull request. GitHub computes it lazily, so it may be
// MergeStateUnknown or empty right after a push.
type MergeState string

// Merge states reported by GitHub.
const (
	MergeStateBehind   MergeState = "BEHIND"
	MergeStateBlocked  MergeState = "BLOCKED"
	MergeStateClean    MergeState = "CLEAN"
	MergeStateDirty    MergeState = "DIRTY"
	MergeStateDraft    MergeState = "DRAFT"
	MergeStateHasHooks MergeState = "HAS_HOOKS"
	MergeStateUnknown  MergeState = "UNKNOWN"
	MergeStateUnstable MergeState = "UNSTABLE"
)

// CheckState is the combined state of the CI checks on the head commit of a pull request. It is empty when the
// commit has no checks.
type CheckState string

// Check states reported by GitHub.
const (
	CheckSuccess  CheckState = "SUCCESS"
	CheckFailure  CheckState = "FAILURE"
	CheckError    CheckState = "ERROR"
	CheckPending  CheckState = "PENDING"
	CheckExpected CheckState = "EXPECTED"
)

// MergeQueueEntry describes the position of a pull request in its repository's merge queue.
type MergeQueueEntry struct {
	// State is the GitHub merge queue entry state, for example QUEUED or AWAITING_CHECKS.
	State string
	// Position is the one-based position in the queue.
	Position int
}

// PullRequest is a Dependabot pull request.
type PullRequest struct {
	// Repository is the repository in owner/name form.
	Repository string
	Number     int
	Title      string
	URL        string
	Author     string
	Draft      bool
	CreatedAt  time.Time
	UpdatedAt  time.Time

	ReviewDecision ReviewDecision
	MergeState     MergeState
	// AutoMerge reports whether auto-merge is enabled.
	AutoMerge bool
	// MergeQueue is set when the pull request is in a merge queue.
	MergeQueue *MergeQueueEntry
	// CI is the combined state of the checks on the head commit.
	CI CheckState
}
