package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// attachmentModule is ticket attachments. Without an object store configured
// (S3_ENDPOINT) the routes answer 503 and there is no sweeper.
type attachmentModule struct {
	Handler *attachment.Handler
	// Jobs is the sweep, none without an object store.
	Jobs []*jobs.Job
}

func newAttachmentModule(d *moduleDeps) (module, error) {
	c := d.container
	cfg := c.cfg.Attachments
	if !cfg.Enabled() {
		return &attachmentModule{Handler: attachment.NewHandler(nil, c.logHandler)}, nil
	}
	// EnsureBucket runs here, so a store that cannot be reached fails startup.
	store, err := attachment.NewS3Store(d.ctx, &attachment.S3Config{
		Endpoint: cfg.S3Endpoint, Region: cfg.S3Region, Bucket: cfg.S3Bucket, CredentialsFile: cfg.S3CredentialsFile,
	})
	if err != nil {
		return nil, err
	}
	repo := attachment.NewRepository(c.database)
	metrics := attachment.NewMetrics()
	if c.limits != nil && c.limits.telemetry != nil {
		if err := c.limits.telemetry.Register(metrics.Collectors()...); err != nil {
			return nil, fmt.Errorf("register attachment metrics: %w", err)
		}
	}
	svc := attachment.NewService(&attachment.Options{
		Repo: repo, Store: store, Quota: attachment.SettingsQuota{Settings: d.svcs.settings},
		Events: c.events, Observer: metrics, TempDir: cfg.TmpDir,
		Holder: ticketHolder{tickets: d.svcs.tickets},
	})
	sweeper, err := attachment.NewSweeper(repo, store, &attachment.SweeperConfig{UploadGrace: cfg.UploadGrace}, c.logHandler)
	if err != nil {
		return nil, err
	}
	return &attachmentModule{Handler: attachment.NewHandler(svc, c.logHandler), Jobs: []*jobs.Job{sweeper.Job(cfg.SweepInterval)}}, nil
}

func (m *attachmentModule) Routes(r *apiserver.Deps) { r.AttachmentHandler = m.Handler }

func (*attachmentModule) Close() {}

// ticketHolder tells the attachment service which ticket a worker holds.
type ticketHolder struct {
	tickets interface {
		LeaseFor(ctx context.Context, agentID uuid.UUID) (*ticket.Lease, error)
	}
}

func (h ticketHolder) HeldTicket(ctx context.Context, agentID uuid.UUID) (int64, bool, error) {
	lease, err := h.tickets.LeaseFor(ctx, agentID)
	if errors.Is(err, ticket.ErrNoLease) || (err == nil && lease == nil) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return lease.TicketID, true, nil
}
