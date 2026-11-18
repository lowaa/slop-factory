package reporter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLabel_PostsVerdictWithToken(t *testing.T) {
	var gotPath, gotAuth string
	var got LabelRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	rep, err := New(srv.URL, "tok", t.TempDir(), slog.DiscardHandler)
	require.NoError(t, err)
	id := uuid.New()

	require.NoError(t, rep.Label(context.Background(), id, &LabelRequest{Verdict: "confirmed", Reason: "r", FixedIn: "abc"}))

	assert.Equal(t, "/api/v1/findings/"+id.String()+"/labels", gotPath)
	assert.Equal(t, "Bearer tok", gotAuth)
	assert.Equal(t, LabelRequest{Verdict: "confirmed", Reason: "r", FixedIn: "abc"}, got)
}

func TestLabel_Errors(t *testing.T) {
	for status, want := range map[int]string{404: "no finding", 401: "refused the token", 500: "returned 500"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		rep, err := New(srv.URL, "tok", t.TempDir(), slog.DiscardHandler)
		require.NoError(t, err)

		err = rep.Label(context.Background(), uuid.New(), &LabelRequest{Verdict: "confirmed"})

		require.Error(t, err)
		assert.Contains(t, err.Error(), want)
		srv.Close()
	}
}
