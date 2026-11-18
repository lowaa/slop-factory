// Package centralconfig fetches the tenant's provider config from
// the report server at the start of a run, keeps the last copy fetched,
// and falls back to that copy when the server can't be reached (see
// config.CentralSource).
package centralconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-review/internal/config"
	"github.com/lowaa/slop-factory/slop-review/report/providerconfig"
	"github.com/lowaa/slop-factory/slop-review/report/reporter"
)

const (
	cacheDirPerm  = 0o700
	cacheFilePerm = 0o600
)

// Fetcher is what Source needs of a reporter.Reporter.
type Fetcher interface {
	FetchProviderConfig(ctx context.Context) (*providerconfig.Config, error)
}

// Source is config.CentralSource against a real report server.
type Source struct {
	// NewFetcher builds the client for one server; nil means a
	// reporter.Reporter, with the same URL and token checks as a report.
	NewFetcher func(reportURL string, tokens reporter.TokenSource) (Fetcher, error)
	LogHandler slog.Handler
	// Now nil means time.Now.
	Now func() time.Time
}

// saved is the last config fetched from one server, as kept on disk.
type saved struct {
	ReportURL string                 `json:"report_url"`
	FetchedAt time.Time              `json:"fetched_at"`
	Config    *providerconfig.Config `json:"config"`
}

func (s *Source) Lookup(ctx context.Context, reportURL string, tokens reporter.TokenSource, cacheDir string) *config.Central {
	reportURL = strings.TrimSuffix(reportURL, "/")
	path := cachePath(cacheDir, reportURL, tokens.Key())
	fetcher, err := s.newFetcher(reportURL, tokens)
	if err != nil {
		return s.fromSaved(path, reportURL, err)
	}
	pc, err := fetcher.FetchProviderConfig(ctx)
	if errors.Is(err, reporter.ErrNoProviderConfig) {
		// The tenant has none (or the server predates them): the local
		// settings are what applies, and an old copy must not outlive a
		// config the server no longer has.
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			s.logger().Warn("could not remove the saved provider config", "path", path, "err", rmErr)
		}
		return &config.Central{}
	}
	if err != nil {
		return s.fromSaved(path, reportURL, err)
	}
	out := &config.Central{Config: pc, Source: config.SourceCentral}
	// Revisions only go up: a concurrent run that fetched an older one
	// must not replace a newer saved copy. (Two writers can still race
	// between this check and the rename; the loser's copy is corrected by
	// the next fetch.)
	if old := s.readSaved(path, reportURL); old != nil && old.Config.Revision > pc.Revision {
		return out
	}
	if err := s.save(path, &saved{ReportURL: reportURL, FetchedAt: s.now(), Config: pc}); err != nil {
		out.Notes = append(out.Notes, fmt.Sprintf("the provider config (revision %d) could not be saved for when the server is unreachable: %v", pc.Revision, err))
	}
	return out
}

// fromSaved is the answer when the server couldn't be asked: the saved copy
// if there is a usable one, the local settings otherwise.
func (s *Source) fromSaved(path, reportURL string, fetchErr error) *config.Central {
	raw, err := os.ReadFile(path)
	if err != nil {
		return &config.Central{Notes: []string{fmt.Sprintf("could not fetch the provider config (%v), and none was saved before: using the local settings", fetchErr)}}
	}
	sv, ok := decodeSaved(raw, reportURL)
	if !ok {
		return &config.Central{Notes: []string{fmt.Sprintf("could not fetch the provider config (%v), and the saved copy is unreadable: using the local settings", fetchErr)}}
	}
	// Checked again: the file is outside this process's control between
	// runs, and must not be a way around the server's own validation.
	if err := sv.Config.Validate(); err != nil {
		return &config.Central{Notes: []string{fmt.Sprintf("could not fetch the provider config (%v), and the saved copy is not valid (%v): using the local settings", fetchErr, err)}}
	}
	return &config.Central{Config: sv.Config, Source: config.SourceSaved, Notes: []string{
		fmt.Sprintf("could not fetch the provider config (%v): using the copy saved at %s (revision %d)",
			fetchErr, sv.FetchedAt.UTC().Format(time.RFC3339), sv.Config.Revision),
	}}
}

// readSaved is the saved copy at path, if it is one for reportURL.
func (s *Source) readSaved(path, reportURL string) *saved {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	sv, ok := decodeSaved(raw, reportURL)
	if !ok {
		return nil
	}
	return sv
}

func decodeSaved(raw []byte, reportURL string) (*saved, bool) {
	var sv saved
	if err := json.Unmarshal(raw, &sv); err != nil || sv.Config == nil || sv.ReportURL != reportURL {
		return nil, false
	}
	return &sv, true
}

// save writes sv atomically, so a run reading the file while another
// writes it never sees half a config.
func (s *Source) save(path string, sv *saved) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, cacheDirPerm); err != nil {
		return err
	}
	raw, err := json.Marshal(sv)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "provider-config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once renamed
	if err := tmp.Chmod(cacheFilePerm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// cachePath keeps each server's and each token's copy apart: a tenant's
// config must only ever be applied to runs reporting to the server, and the
// tenant, it came from -- two checkouts on one machine can report to
// different tenants. Only a hash of the token (or of its command) is in the name.
func cachePath(dir, reportURL, tokenKey string) string {
	sum := sha256.Sum256([]byte(reportURL + "\n" + tokenKey))
	return filepath.Join(dir, hex.EncodeToString(sum[:8])+".json")
}

func (s *Source) newFetcher(reportURL string, tokens reporter.TokenSource) (Fetcher, error) {
	if s.NewFetcher != nil {
		return s.NewFetcher(reportURL, tokens)
	}
	// No spool: this reporter only fetches.
	return reporter.New(reportURL, "", "", s.handler(), reporter.WithTokenSource(tokens))
}

func (s *Source) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Source) handler() slog.Handler {
	if s.LogHandler != nil {
		return s.LogHandler
	}
	return slog.DiscardHandler
}

func (s *Source) logger() *slog.Logger {
	return slog.New(s.handler())
}
