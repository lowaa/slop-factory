package agent_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorwatch"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// boardSwitch is a settings service that imports through the real provisioning
// service, as main wires it, with the flag starting off.
func (p *provisioning) boardSwitch(t *testing.T, hook settings.ProvisionEnabledHook) *settings.Service {
	t.Helper()
	_, err := p.settings.Set(p.ctx, settings.ProvisionFromBoard, settings.BoolValue(false), p.user.ID)
	require.NoError(t, err)
	return settings.NewService(settings.NewPostgresStore(p.database), audit.New(slog.DiscardHandler, nil), slog.DiscardHandler,
		settings.WithOwners(tenantRoles{user.NewPostgresStore(p.database)}), settings.WithProvisionEnabledHook(hook))
}

type failingImport struct{ calls int }

func (f *failingImport) ImportAll(context.Context) (int, error) {
	f.calls++
	return 0, errors.New("boom")
}

func TestTurningProvisionFromBoardOnImportsEveryReportedSlot(t *testing.T) {
	p := newProvisioning(t)
	svc := p.boardSwitch(t, p.provision)
	other := p.supervisor(t, "other-box", 4)
	p.reportedSlot(t, p.sup, "a", supervisorwatch.PhaseIdle)
	p.reportedSlot(t, other, "b", supervisorwatch.PhasePaused)

	_, err := svc.Set(p.ctx, settings.ProvisionFromBoard, settings.BoolValue(true), p.user.ID)

	require.NoError(t, err)
	assert.Len(t, p.read(t).Agents, 1)
	theirs, err := p.provision.OwnerView(p.ctx, p.user, other.ID)
	require.NoError(t, err)
	assert.Len(t, theirs.Agents, 1)
}

func TestProvisionFromBoardStaysOffWhenTheImportFails(t *testing.T) {
	p := newProvisioning(t)
	hook := &failingImport{}
	svc := p.boardSwitch(t, hook)

	_, err := svc.Set(p.ctx, settings.ProvisionFromBoard, settings.BoolValue(true), p.user.ID)

	require.Error(t, err)
	assert.Equal(t, 1, hook.calls)
	on, err := svc.ProvisionFromBoardEnabled(p.ctx)
	require.NoError(t, err)
	assert.False(t, on)
}

func TestOnlyTheOffToOnFlipImports(t *testing.T) {
	p := newProvisioning(t)
	hook := &failingImport{}
	svc := p.boardSwitch(t, hook)

	_, err := svc.Set(p.ctx, settings.ProvisionFromBoard, settings.BoolValue(false), p.user.ID)
	require.NoError(t, err, "off to off")
	p.reportedSlot(t, p.sup, "a", supervisorwatch.PhaseIdle)
	require.NoError(t, errors.Join(setBoard(p, p.settings, true), setBoard(p, svc, true), setBoard(p, svc, false)), "on to on and on to off")

	assert.Zero(t, hook.calls)
	assert.Empty(t, p.read(t).Agents)
}

func setBoard(p *provisioning, svc *settings.Service, on bool) error {
	_, err := svc.Set(p.ctx, settings.ProvisionFromBoard, settings.BoolValue(on), p.user.ID)
	return err
}
