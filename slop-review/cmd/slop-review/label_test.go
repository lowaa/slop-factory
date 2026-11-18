package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedLabel struct {
	path, auth string
	body       map[string]string
}

// labelAPI is a fake of the label endpoint that only records requests.
func labelAPI(t *testing.T, status int) (*httptest.Server, *[]recordedLabel) {
	t.Helper()
	var reqs []recordedLabel
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := map[string]string{}
		_ = json.Unmarshal(b, &body)
		reqs = append(reqs, recordedLabel{r.URL.Path, r.Header.Get("Authorization"), body})
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func TestLabel_PostsToTheFindingWithTheIngestToken(t *testing.T) {
	srv, reqs := labelAPI(t, http.StatusCreated)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "tok")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	id := uuid.New()
	var out, errOut bytes.Buffer

	err := run([]string{"label", id.String(), "--verdict=confirmed", "--reason=real", "--fixed-in=abc123", "--report-to=" + srv.URL}, &out, &errOut)

	require.NoError(t, err)
	require.Len(t, *reqs, 1)
	assert.Equal(t, "/api/v1/findings/"+id.String()+"/labels", (*reqs)[0].path)
	assert.Equal(t, "Bearer tok", (*reqs)[0].auth)
	assert.Equal(t, map[string]string{"verdict": "confirmed", "reason": "real", "fixed_in": "abc123"}, (*reqs)[0].body)
	assert.Contains(t, out.String(), "labelled "+id.String())
}

func TestLabel_ServerFailureWarnsAndSucceeds(t *testing.T) {
	srv, _ := labelAPI(t, http.StatusInternalServerError)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "tok")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	var out, errOut bytes.Buffer

	err := run([]string{"label", uuid.NewString(), "--verdict=false_positive", "--report-to=" + srv.URL}, &out, &errOut)

	require.NoError(t, err)
	assert.Contains(t, errOut.String(), "label not recorded")
	assert.NotContains(t, errOut.String(), "tok\n")
}

func TestLabel_UsageErrorsPostNothing(t *testing.T) {
	srv, reqs := labelAPI(t, http.StatusCreated)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "tok")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	id := uuid.NewString()
	for name, args := range map[string][]string{
		"bad id":               {"label", "nope", "--verdict=confirmed"},
		"bad verdict":          {"label", id, "--verdict=maybe"},
		"no verdict":           {"label", id},
		"fixed-in unconfirmed": {"label", id, "--verdict=duplicate", "--fixed-in=abc"},
		"no id":                {"label", "--verdict=confirmed"},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			require.Error(t, run(append(args, "--report-to="+srv.URL), &out, &errOut))
		})
	}
	assert.Empty(t, *reqs)
}

func TestLabel_RequiresServerAndToken(t *testing.T) {
	id := uuid.NewString()
	var out, errOut bytes.Buffer

	t.Setenv("SLOP_REVIEW_REPORT_URL", "")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "tok")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	err := run([]string{"label", id, "--verdict=confirmed"}, &out, &errOut)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no server")

	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	err = run([]string{"label", id, "--verdict=confirmed", "--report-to=http://127.0.0.1:1"}, &out, &errOut)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SLOP_REVIEW_REPORT_TOKEN")
}
