package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// batchDeps are what the batch runner is built from beyond the landing runner.
type batchDeps struct {
	database  *db.DB
	manifests manifest.Store
	settings  landing.SettingReader
	events    *audit.Recorder
}

// newBatchRunner is the batch runner for r. The tenant's merge_queue_batch_size
// decides whether it batches at all: at 1, the default, every landing is serial.
func newBatchRunner(r *landing.Runner, host *landing.PostgresHost, d *batchDeps, logHandler slog.Handler) (*landing.BatchRunner, error) {
	store := landing.NewBatchStore(d.database)
	builder, err := landing.NewBatchBuilder(r, &landing.BatchOptions{
		Store: store, Host: host, Members: landing.NewPostgresMemberHandBack(d.database),
	})
	if err != nil {
		return nil, err
	}
	return landing.NewBatchRunner(r, &landing.BatchRunnerOptions{
		Builder:  builder,
		Selector: landing.NewBatchSelectorFrom(manifestBatchPolicy{manifests: d.manifests}, host),
		Queue:    landing.NewPostgresQueue(d.database),
		Checks:   host,
		Recovery: landing.NewPostgresBatchRecovery(d.database),
		Size: func(ctx context.Context) (int, error) {
			s, err := d.settings.Get(ctx, settings.MergeQueueBatchSize)
			if err != nil {
				return 0, err
			}
			return int(s.Value.Int), nil
		},
		Alert: func(ctx context.Context, ticketID, batchID int64, why string) {
			d.events.Record(ctx, audit.BatchTreeMismatch, "ticket_id", ticketID, "batch_id", batchID, "why", why)
		},
	})
}

// manifestBatchPolicy batches changes within the apps the manifest declares
// independent and enforced. A file outside every such app (CI's definition, the
// test harness, shared code) is solo: other changes' tests depend on it, so one
// green run of a batch holding it says nothing for the changes after it. With
// no enforced app declared, or with enforced apps in more than one repo,
// nothing is batched: Solo reads repo-relative files and cannot tell whose
// apps they are in.
type manifestBatchPolicy struct {
	manifests manifest.Store
}

func (p manifestBatchPolicy) Policy(ctx context.Context) (landing.BatchPolicy, error) {
	m, err := p.manifests.Get(ctx)
	if err != nil {
		return nil, err
	}
	return newManifestPolicy(m), nil
}

// manifestPolicy is a landing.BatchPolicy: App reads workspace paths, Solo
// repo-relative ones.
type manifestPolicy struct {
	workspace landing.PathPolicy
	repoApps  []string
	// migrationDirs are repo-relative rules of the shared paths resolved as
	// migrations, of the enforcing repo.
	migrationDirs []string
}

func newManifestPolicy(m *manifest.Manifest) *manifestPolicy {
	p := &manifestPolicy{}
	if m == nil {
		return p
	}
	enforcing := 0
	for i := range m.Repos {
		if m.Repos[i].BatchWithinApps && len(m.Repos[i].IndependentApps) > 0 {
			enforcing++
		}
	}
	if enforcing != 1 {
		return p
	}
	for i := range m.Repos {
		r := &m.Repos[i]
		if !r.BatchWithinApps {
			continue
		}
		for _, app := range r.IndependentApps {
			p.workspace.Apps = append(p.workspace.Apps, r.Path+"/"+app)
			p.repoApps = append(p.repoApps, app)
		}
		for _, rule := range r.SharedPaths {
			if rule.Resolve == scope.ResolveMigration {
				p.migrationDirs = append(p.migrationDirs, rule.Path)
			}
		}
	}
	return p
}

func (p *manifestPolicy) App(paths []string) (string, bool) { return p.workspace.App(paths) }

func (p *manifestPolicy) Solo(file string) bool {
	for _, app := range p.repoApps {
		if strings.HasPrefix(file, app) {
			return false
		}
	}
	return true
}

func (p *manifestPolicy) Migration(file string) (string, bool) {
	for _, rule := range p.migrationDirs {
		if scope.PathRuleMatches(rule, file) {
			return rule, true
		}
	}
	return "", false
}

var _ landing.BatchPolicy = (*manifestPolicy)(nil)
