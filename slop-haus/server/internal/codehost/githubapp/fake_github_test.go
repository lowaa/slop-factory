package githubapp_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub stands in for GitHub's API. It decides nothing: it answers
// each route with what the test configured and records what it was asked.
// Whether a request was right is the test's to assert from the record.
type fakeGitHub struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []recordedRequest

	// What each route answers; a zero Status is the route's success status.
	installation answer
	// installationByID overrides installation for one id.
	installationByID map[int64]answer
	// installations is GET /app/installations; conversion is the manifest
	// code exchange.
	installations answer
	conversion    answer
	token         func(call int) answer
	repos         func(page int) answer
	tokenCalls    int
	// hookConfig answers GET /app/hook/config, hookPatch PATCH to it and
	// hookDeliveries GET /app/hook/deliveries.
	hookConfig     answer
	hookPatch      answer
	hookDeliveries answer
}

type answer struct {
	Status int
	Body   any
}

type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Body   []byte
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) URL() string { return f.srv.URL }

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), body})
	var ans answer
	switch {
	case strings.HasSuffix(r.URL.Path, "/access_tokens"):
		f.tokenCalls++
		if f.token != nil {
			ans = f.token(f.tokenCalls)
		}
		if ans.Status == 0 {
			ans.Status = http.StatusCreated
		}
	case r.URL.Path == "/installation/repositories":
		if f.repos != nil {
			page := 1
			if p := r.URL.Query().Get("page"); p != "" {
				page, _ = strconv.Atoi(p)
			}
			ans = f.repos(page)
		}
	case strings.HasPrefix(r.URL.Path, "/app-manifests/"):
		ans = f.conversion
		if ans.Status == 0 {
			ans.Status = http.StatusCreated
		}
	case r.URL.Path == "/app/hook/config" && r.Method == http.MethodPatch:
		ans = f.hookPatch
	case r.URL.Path == "/app/hook/config":
		ans = f.hookConfig
	case r.URL.Path == "/app/hook/deliveries":
		ans = f.hookDeliveries
	case r.URL.Path == "/app/installations":
		ans = f.installations
	default:
		ans = f.installation
		if id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/app/installations/"), 10, 64); err == nil {
			if o, ok := f.installationByID[id]; ok {
				ans = o
			}
		}
	}
	f.mu.Unlock()
	if ans.Status == 0 {
		ans.Status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(ans.Status)
	_ = json.NewEncoder(w).Encode(ans.Body)
}

// Calls are the recorded requests whose path ends with suffix.
func (f *fakeGitHub) Calls(suffix string) []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedRequest
	for _, r := range f.requests {
		if strings.HasSuffix(r.Path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeGitHub) Total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// tokenBody is a successful access_tokens answer.
func tokenBody(token string, expires time.Time, repos ...string) map[string]any {
	list := []map[string]string{}
	for _, r := range repos {
		list = append(list, map[string]string{"full_name": r})
	}
	return map[string]any{"token": token, "expires_at": expires.UTC().Format(time.RFC3339), "repositories": list}
}
