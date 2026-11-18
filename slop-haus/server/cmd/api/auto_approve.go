package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usersettings"
)

// autoApproveSettingFor names the tenant setting that switches auto-approve
// on for each kind of proposal. A kind missing from it is never
// auto-approved; TestEveryProposalKindHasAnAutoApproveSetting keeps a new
// kind from being forgotten.
var autoApproveSettingFor = map[pendingaction.Kind]string{
	pendingaction.KindStatus:           usersettings.ApprovalsAutoStatus,
	pendingaction.KindEdit:             usersettings.ApprovalsAutoEdit,
	pendingaction.KindReady:            usersettings.ApprovalsAutoReady,
	pendingaction.KindReviewDecision:   usersettings.ApprovalsAutoReviewDecision,
	pendingaction.KindDiscussionAnswer: usersettings.ApprovalsAutoDiscussionAnswer,
}

// tenantBools reads one user's tenant settings. Satisfied by *usersettings.TenantService.
type tenantBools interface {
	Get(ctx context.Context, userID uuid.UUID, name string) (*usersettings.Setting, error)
}

type autoApproveSettings struct{ settings tenantBools }

func newAutoApproveSettings(settings tenantBools) *autoApproveSettings {
	return &autoApproveSettings{settings: settings}
}

func (a *autoApproveSettings) AutoApprove(ctx context.Context, userID uuid.UUID, kind pendingaction.Kind) (bool, error) {
	name, ok := autoApproveSettingFor[kind]
	if !ok {
		return false, nil
	}
	got, err := a.settings.Get(ctx, userID, name)
	if err != nil {
		return false, fmt.Errorf("read %s: %w", name, err)
	}
	return got.Value.Type == usersettings.TypeBool && got.Value.Bool, nil
}
