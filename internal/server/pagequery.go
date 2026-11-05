package server

import (
	"net/url"
	"slices"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
)

// queryRepo is the query parameter that limits the table to one repository.
const queryRepo = "repo"

// pageQuery is the state of the page that the URL carries: how the table is sorted and which repository it is limited
// to. Every link of the page keeps it, so that it survives sorting, refreshing and the automatic reload.
type pageQuery struct {
	order order
	// repo is the full owner/name of the only repository to show pull requests from. It is empty to show them all.
	repo string
}

// parsePageQuery returns the page state requested by query.
func parsePageQuery(query url.Values) pageQuery {
	return pageQuery{order: parseOrder(query), repo: query.Get(queryRepo)}
}

// path returns the URL of base that keeps the page in state p.
func (p pageQuery) path(base string) string {
	values := p.order.values()
	if p.repo != "" {
		values.Set(queryRepo, p.repo)
	}
	if len(values) == 0 {
		return base
	}
	return base + "?" + values.Encode()
}

// withOrder returns p sorted by o.
func (p pageQuery) withOrder(o order) pageQuery {
	p.order = o
	return p
}

// withRepo returns p limited to repo, or to no repository when repo is empty.
func (p pageQuery) withRepo(repo string) pageQuery {
	p.repo = repo
	return p
}

// filter returns the pull requests of prs that p shows.
func (p pageQuery) filter(prs []ghclient.PullRequest) []ghclient.PullRequest {
	if p.repo == "" {
		return prs
	}
	return slices.DeleteFunc(slices.Clone(prs), func(pr ghclient.PullRequest) bool { return pr.Repository != p.repo })
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

// headers returns the column headings of the table in state p.
func (p pageQuery) headers() []header {
	headers := make([]header, 0, len(columns()))
	for _, c := range columns() {
		h := header{Label: c.label(), Href: p.withOrder(p.order.clicked(c)).path("/")}
		switch {
		case p.order.column != c:
		case p.order.descending:
			h.Sort, h.Arrow = "descending", "▼"
		default:
			h.Sort, h.Arrow = "ascending", "▲"
		}
		headers = append(headers, h)
	}
	return headers
}

// repoFilter describes the repository the table is limited to.
type repoFilter struct {
	Repository string
	// ClearHref is the page without the filter.
	ClearHref string
}
