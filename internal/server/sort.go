package server

import (
	"cmp"
	"net/url"
	"slices"
	"strings"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
)

// Query parameters that carry the sort order, and the values of the direction.
const (
	querySort = "sort"
	queryDir  = "dir"

	dirAscending  = "asc"
	dirDescending = "desc"
)

// column is a sortable table column. Its value is what the sort query parameter carries.
type column string

// Table columns.
const (
	columnRepository column = "repo"
	columnTitle      column = "title"
	columnAge        column = "age"
	columnReview     column = "review"
	columnStatus     column = "status"
	columnCI         column = "ci"
)

// columns returns the sortable columns in the order the table shows them.
func columns() []column {
	return []column{columnRepository, columnTitle, columnAge, columnReview, columnStatus, columnCI}
}

// label returns the column heading.
func (c column) label() string {
	switch c {
	case columnTitle:
		return "Pull request"
	case columnAge:
		return "Age"
	case columnReview:
		return "Review"
	case columnStatus:
		return "Status"
	case columnCI:
		return "CI"
	default:
		return "Repository"
	}
}

// startsDescending reports whether the first click on the column sorts it descending. That is the case for age, so
// that the oldest pull request comes first. The other columns start with the first value of their order.
func (c column) startsDescending() bool {
	return c == columnAge
}

// order is how the rows of the table are sorted.
type order struct {
	column     column
	descending bool
}

// defaultOrder is the order of a page that was requested without a sort order.
func defaultOrder() order {
	return order{column: columnRepository}
}

// parseOrder returns the order requested by query. Anything it does not recognize falls back to a default, so that a
// mistyped URL still shows the table.
func parseOrder(query url.Values) order {
	i := slices.Index(columns(), column(query.Get(querySort)))
	if i < 0 {
		return defaultOrder()
	}
	// Take the column from the known ones, so that nothing the client sent ends up in the order.
	c := columns()[i]
	switch query.Get(queryDir) {
	case dirAscending:
		return order{column: c}
	case dirDescending:
		return order{column: c, descending: true}
	default:
		return order{column: c, descending: c.startsDescending()}
	}
}

// query returns o as a URL query. It is empty for the default order.
func (o order) query() string {
	if o == defaultOrder() {
		return ""
	}
	dir := dirAscending
	if o.descending {
		dir = dirDescending
	}
	return url.Values{querySort: {string(o.column)}, queryDir: {dir}}.Encode()
}

// path returns the URL of base that keeps the table sorted by o.
func (o order) path(base string) string {
	query := o.query()
	if query == "" {
		return base
	}
	return base + "?" + query
}

// clicked returns the order that results from clicking the heading of c.
func (o order) clicked(c column) order {
	if o.column == c {
		return order{column: c, descending: !o.descending}
	}
	return order{column: c, descending: c.startsDescending()}
}

// header is a column heading of the table. It links to the table sorted by that column.
type header struct {
	Label string
	Href  string
	// Sort is the aria-sort value of the heading. It is empty unless the table is sorted by the column.
	Sort string
	// Arrow shows the sort direction. It is empty unless the table is sorted by the column.
	Arrow string
}

// headers returns the column headings of a table sorted by o.
func (o order) headers() []header {
	headers := make([]header, 0, len(columns()))
	for _, c := range columns() {
		h := header{Label: c.label(), Href: o.clicked(c).path("/")}
		switch {
		case o.column != c:
		case o.descending:
			h.Sort, h.Arrow = "descending", "▼"
		default:
			h.Sort, h.Arrow = "ascending", "▲"
		}
		headers = append(headers, h)
	}
	return headers
}

// sortPullRequests returns a copy of prs sorted by o. Pull requests that are equal in the sorted column keep a stable
// order: by repository, then by number.
func sortPullRequests(prs []ghclient.PullRequest, o order) []ghclient.PullRequest {
	sorted := slices.Clone(prs)
	slices.SortStableFunc(sorted, func(a, b ghclient.PullRequest) int {
		if c := compareColumn(o.column, a, b); c != 0 {
			if o.descending {
				return -c
			}
			return c
		}
		return cmp.Or(cmp.Compare(a.Repository, b.Repository), cmp.Compare(a.Number, b.Number))
	})
	return sorted
}

// compareColumn compares a and b by column c, in ascending order.
func compareColumn(c column, a, b ghclient.PullRequest) int {
	switch c {
	case columnTitle:
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	case columnAge:
		// The older a pull request, the greater its age.
		return b.CreatedAt.Compare(a.CreatedAt)
	case columnReview:
		return cmp.Compare(reviewRank(a.ReviewDecision), reviewRank(b.ReviewDecision))
	case columnStatus:
		return cmp.Compare(statusRank(a), statusRank(b))
	case columnCI:
		return cmp.Compare(ciRank(a.CI), ciRank(b.CI))
	default:
		return cmp.Compare(a.Repository, b.Repository)
	}
}

// rank returns the position of label in labels, or after all of them when label is not listed.
func rank(label string, labels []string) int {
	if i := slices.Index(labels, label); i >= 0 {
		return i
	}
	return len(labels)
}

// reviewRank ranks decisions by how close the pull request is to being merged. It follows the badge shown, so what
// looks the same sorts the same.
func reviewRank(decision ghclient.ReviewDecision) int {
	return rank(
		reviewBadge(decision).Label,
		[]string{labelApproved, labelReviewNone, labelReviewRequired, labelChangesRequested},
	)
}

// statusRank ranks merge states by how close the pull request is to being merged.
func statusRank(pr ghclient.PullRequest) int {
	return rank(
		statusBadge(pr).Label,
		[]string{labelReady, labelUnstable, labelBehind, labelBlocked, labelConflicts, labelDraft, labelStatusUnset},
	)
}

// ciRank ranks check states from passing to failing.
func ciRank(state ghclient.CheckState) int {
	return rank(
		ciBadge(state).Label,
		[]string{labelPassing, labelRunning, labelNoChecks, labelFailing},
	)
}
