package server

import (
	"encoding/json"
	"mime"
	"net/http"
	"slices"

	"github.com/Edholm/gh-dependabot-triage/internal/ghclient"
)

// maxQueueRequestBytes bounds the body of a queue request. It only lists pull requests.
const maxQueueRequestBytes = 1 << 20

// queueRequest asks the queue to approve or merge pull requests.
type queueRequest struct {
	Action action `json:"action"`
	// PullRequests are keys of pull requests, see pullRequestKey.
	PullRequests []string `json:"pullRequests"`
}

// queueRemoveRequest asks the queue to drop a pull request it has not started working on.
type queueRemoveRequest struct {
	Key string `json:"key"`
}

func (s *Server) handleQueue(w http.ResponseWriter, _ *http.Request) {
	s.writeQueue(w)
}

func (s *Server) handleQueueAdd(w http.ResponseWriter, r *http.Request) {
	var req queueRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Action != actionApprove && req.Action != actionMerge {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	// Pull requests are looked up in what the server fetched, so only pull requests the page shows can be changed.
	// Keep the order the page asked for: it is the order to merge in.
	prs := s.snapshot().prs
	var selected []ghclient.PullRequest
	for _, key := range req.PullRequests {
		i := slices.IndexFunc(prs, func(pr ghclient.PullRequest) bool { return pullRequestKey(pr) == key })
		if i >= 0 {
			selected = append(selected, prs[i])
		}
	}
	if len(selected) == 0 {
		http.Error(w, "none of the pull requests are open", http.StatusUnprocessableEntity)
		return
	}
	if err := s.queue.add(r.Context(), req.Action, selected); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	s.writeQueue(w)
}

func (s *Server) handleQueueRemove(w http.ResponseWriter, r *http.Request) {
	var req queueRemoveRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !s.queue.remove(req.Key) {
		http.Error(w, "the pull request is not waiting in the queue", http.StatusConflict)
		return
	}
	s.writeQueue(w)
}

func (s *Server) handleQueueClear(w http.ResponseWriter, _ *http.Request) {
	s.queue.clearFinished()
	s.writeQueue(w)
}

func (s *Server) writeQueue(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(s.queue.view()); err != nil {
		s.logger.Error("write queue", "error", err)
	}
}

// decodeJSON decodes the body of r into v. If that fails, it answers the request and returns false.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxQueueRequestBytes)).Decode(v); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

// requireSameOriginJSON rejects requests that a page of another origin could have sent. The Host check of
// requireLoopbackHost does not stop a website from posting a form to the server, so requests that change pull
// requests must carry a JSON body, which browsers only send cross-origin after a CORS preflight that this server
// never allows, and must not come from another origin.
func requireSameOriginJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			http.Error(w, "expected a JSON body", http.StatusUnsupportedMediaType)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
