package logging_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"

	"github.com/lowaa/slop-factory/go-auth/audit"
	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/logging"
)

// logStore stands in for VictoriaLogs' OTLP endpoint, keeping every body
// it is sent. OTLP/HTTP bodies are protobuf, where strings are stored as
// they are, so a message can be looked for in the raw bytes.
type logStore struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (s *logStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.bodies = append(s.bodies, body)
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *logStore) all() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Join(s.bodies, nil)
}

// The policy is shared (go-privacy/otellog); this is the api's audit and error logs through
// it: what must not be logged is in neither the stderr copy nor the exported one.
func TestThePolicyAppliesToStderrAndTheLogStore(t *testing.T) {
	const (
		fixtureEmail = "ann.patient@example.com"
		fixtureIP    = "203.0.113.77"
		fixtureTitle = "Quarterly oncology review for Ann Patient"
	)
	store := &logStore{}
	srv := httptest.NewServer(store)
	defer srv.Close()
	var stderr bytes.Buffer
	logs, err := logging.New(srv.URL+"/v1/logs", &stderr)
	require.NoError(t, err)

	ctx := clientip.NewContext(context.Background(), netip.MustParseAddr(fixtureIP))
	audit.New(&audit.Config{Logger: logs.Handler()}).Record(ctx, "sign_in_refused", "email", fixtureEmail, "body", "form=secret")
	logger := slog.New(logs.Handler())
	logger.Error("update ticket", "err", fmt.Errorf("ticket: %w: no ticket titled %q", errors.New("not found"), fixtureTitle))
	require.NoError(t, logs.Close(context.Background()))

	for name, got := range map[string]string{"stderr": stderr.String(), "exported": string(store.all())} {
		assert.NotContains(t, got, fixtureEmail, name)
		assert.Regexp(t, `h:[0-9a-f]{16}`, got, name)
		assert.NotContains(t, got, fixtureIP, name)
		assert.NotContains(t, got, "Ann Patient", name)
		assert.NotContains(t, got, "form=secret", name)
		assert.Contains(t, got, "203.0.113.0/24", name)
		assert.Contains(t, got, "sign_in_refused", name)
		assert.Contains(t, got, "no ticket titled", name)
	}
}

func TestNewWritesTextToStderrUnderTheApisServiceName(t *testing.T) {
	store := &logStore{}
	srv := httptest.NewServer(store)
	defer srv.Close()
	var stderr bytes.Buffer
	logs, err := logging.New(srv.URL+"/v1/logs", &stderr)
	require.NoError(t, err)

	slog.New(logs.Handler()).Info("audit", "action", "sign_in")
	require.NoError(t, logs.Close(context.Background()))

	assert.Contains(t, stderr.String(), "action=sign_in")
	assert.Contains(t, string(store.all()), logging.ServiceName)
}

func TestDevMailHandlerIsUnfiltered(t *testing.T) {
	var out bytes.Buffer
	slog.New(logging.DevMailHandler(&out)).Info("email", "to", "ann@example.com", "body", "https://app/activate?token=abc")
	assert.Contains(t, out.String(), "ann@example.com")
	assert.Contains(t, out.String(), "token=abc")
}
