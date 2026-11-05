package server

import (
	"fmt"
	"strings"
	"time"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
)

// Badge classes. They map to CSS classes in static/style.css.
const (
	classGood    = "good"
	classBad     = "bad"
	classWarn    = "warn"
	classNeutral = "neutral"
	classInfo    = "info"
)

// Badge labels. The sort order of the review, status and CI columns is defined in terms of them, see sort.go.
const (
	labelApproved         = "Approved"
	labelChangesRequested = "Changes requested"
	labelReviewRequired   = "Review required"
	labelReviewNone       = "None"

	labelDraft       = "Draft"
	labelConflicts   = "Conflicts"
	labelBehind      = "Behind"
	labelBlocked     = "Blocked"
	labelUnstable    = "Unstable"
	labelReady       = "Ready"
	labelStatusUnset = "Unknown"

	labelPassing  = "✓ Passing"
	labelFailing  = "✗ Failing"
	labelRunning  = "● Running"
	labelNoChecks = "– No checks"
)

// badge is a short colored label. The label always carries the meaning, color is only reinforcement.
type badge struct {
	Label string
	Class string
}

// row is the presentation model of one pull request in the table.
type row struct {
	// Repository is the full owner/name, Owner and Name are its two parts.
	Repository string
	Owner      string
	Name       string
	Number     int
	Title      string
	URL        string
	Age        string
	AgeTitle   string
	Review     badge
	Status     badge
	Extras     []badge
	CI         badge
}

// newRows returns the rows of prs, sorted by o.
func newRows(now time.Time, prs []ghclient.PullRequest, o order) []row {
	rows := make([]row, 0, len(prs))
	for _, pr := range sortPullRequests(prs, o) {
		owner, name, found := strings.Cut(pr.Repository, "/")
		if !found {
			owner, name = "", pr.Repository
		}
		rows = append(rows, row{
			Repository: pr.Repository,
			Owner:      owner,
			Name:       name,
			Number:     pr.Number,
			Title:      pr.Title,
			URL:        pr.URL,
			Age:        relativeAge(now, pr.CreatedAt),
			AgeTitle: fmt.Sprintf(
				"Opened %s, updated %s",
				pr.CreatedAt.Format(time.DateTime),
				pr.UpdatedAt.Format(time.DateTime),
			),
			Review: reviewBadge(pr.ReviewDecision),
			Status: statusBadge(pr),
			Extras: extraBadges(pr),
			CI:     ciBadge(pr.CI),
		})
	}
	return rows
}

func reviewBadge(decision ghclient.ReviewDecision) badge {
	switch decision {
	case ghclient.ReviewApproved:
		return badge{Label: labelApproved, Class: classGood}
	case ghclient.ReviewChangesRequested:
		return badge{Label: labelChangesRequested, Class: classBad}
	case ghclient.ReviewRequired:
		return badge{Label: labelReviewRequired, Class: classWarn}
	default:
		return badge{Label: labelReviewNone, Class: classNeutral}
	}
}

func statusBadge(pr ghclient.PullRequest) badge {
	if pr.Draft {
		return badge{Label: labelDraft, Class: classNeutral}
	}
	switch pr.MergeState {
	case ghclient.MergeStateDraft:
		return badge{Label: labelDraft, Class: classNeutral}
	case ghclient.MergeStateDirty:
		return badge{Label: labelConflicts, Class: classBad}
	case ghclient.MergeStateBehind:
		return badge{Label: labelBehind, Class: classWarn}
	case ghclient.MergeStateBlocked:
		return badge{Label: labelBlocked, Class: classWarn}
	case ghclient.MergeStateUnstable:
		return badge{Label: labelUnstable, Class: classWarn}
	case ghclient.MergeStateClean, ghclient.MergeStateHasHooks:
		return badge{Label: labelReady, Class: classGood}
	default:
		return badge{Label: labelStatusUnset, Class: classNeutral}
	}
}

func extraBadges(pr ghclient.PullRequest) []badge {
	var extras []badge
	if pr.AutoMerge {
		extras = append(extras, badge{Label: "Auto-merge", Class: classInfo})
	}
	if pr.MergeQueue != nil {
		extras = append(
			extras,
			badge{Label: fmt.Sprintf("In merge queue #%d", pr.MergeQueue.Position), Class: classInfo},
		)
	}
	return extras
}

func ciBadge(state ghclient.CheckState) badge {
	switch state {
	case ghclient.CheckSuccess:
		return badge{Label: labelPassing, Class: classGood}
	case ghclient.CheckFailure, ghclient.CheckError:
		return badge{Label: labelFailing, Class: classBad}
	case ghclient.CheckPending, ghclient.CheckExpected:
		return badge{Label: labelRunning, Class: classWarn}
	default:
		return badge{Label: labelNoChecks, Class: classNeutral}
	}
}

// relativeAge formats the time elapsed since t using its largest whole unit, for example "3d".
func relativeAge(now, t time.Time) string {
	elapsed := now.Sub(t)
	switch {
	case elapsed < time.Minute:
		return "now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed/time.Minute))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh", int(elapsed/time.Hour))
	case elapsed < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(elapsed/(24*time.Hour)))
	case elapsed < 365*24*time.Hour:
		return fmt.Sprintf("%dw", int(elapsed/(7*24*time.Hour)))
	default:
		return fmt.Sprintf("%dy", int(elapsed/(365*24*time.Hour)))
	}
}
