package server

import (
	"fmt"
	"sort"
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

func newRows(now time.Time, prs []ghclient.PullRequest) []row {
	sorted := make([]ghclient.PullRequest, len(prs))
	copy(sorted, prs)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Repository != sorted[j].Repository {
			return sorted[i].Repository < sorted[j].Repository
		}
		return sorted[i].Number < sorted[j].Number
	})
	rows := make([]row, 0, len(sorted))
	for _, pr := range sorted {
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
		return badge{Label: "Approved", Class: classGood}
	case ghclient.ReviewChangesRequested:
		return badge{Label: "Changes requested", Class: classBad}
	case ghclient.ReviewRequired:
		return badge{Label: "Review required", Class: classWarn}
	default:
		return badge{Label: "None", Class: classNeutral}
	}
}

func statusBadge(pr ghclient.PullRequest) badge {
	if pr.Draft {
		return badge{Label: "Draft", Class: classNeutral}
	}
	switch pr.MergeState {
	case ghclient.MergeStateDraft:
		return badge{Label: "Draft", Class: classNeutral}
	case ghclient.MergeStateDirty:
		return badge{Label: "Conflicts", Class: classBad}
	case ghclient.MergeStateBehind:
		return badge{Label: "Behind", Class: classWarn}
	case ghclient.MergeStateBlocked:
		return badge{Label: "Blocked", Class: classWarn}
	case ghclient.MergeStateUnstable:
		return badge{Label: "Unstable", Class: classWarn}
	case ghclient.MergeStateClean, ghclient.MergeStateHasHooks:
		return badge{Label: "Ready", Class: classGood}
	default:
		return badge{Label: "Unknown", Class: classNeutral}
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
		return badge{Label: "✓ Passing", Class: classGood}
	case ghclient.CheckFailure, ghclient.CheckError:
		return badge{Label: "✗ Failing", Class: classBad}
	case ghclient.CheckPending, ghclient.CheckExpected:
		return badge{Label: "● Running", Class: classWarn}
	default:
		return badge{Label: "– No checks", Class: classNeutral}
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
