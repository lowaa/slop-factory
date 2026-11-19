package githubapp

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// A sentinel missing from the table would answer 500.
func TestErrTableCoversEverySentinel(t *testing.T) {
	httperr.RequireCovered(t, &errTable, map[string]error{
		"ErrTooManyRepos":          ErrTooManyRepos,
		"ErrBadResponse":           ErrBadResponse,
		"ErrUnreachable":           ErrUnreachable,
		"ErrInvalidKey":            ErrInvalidKey,
		"ErrBadState":              ErrBadState,
		"ErrWrongApp":              ErrWrongApp,
		"ErrNotInstalled":          ErrNotInstalled,
		"ErrAmbiguousInstallation": ErrAmbiguousInstallation,
		"ErrAlreadyInstalled":      ErrAlreadyInstalled,
		"ErrDisabled":              ErrDisabled,
		"ErrNotOwner":              ErrNotOwner,
		"ErrInvalidInput":          ErrInvalidInput,
		"ErrCredentialChanged":     ErrCredentialChanged,
		"ErrStoredKeyUnreadable":   ErrStoredKeyUnreadable,
		"ErrRepoNotCovered":        ErrRepoNotCovered,
		"ErrWebhookUnavailable":    ErrWebhookUnavailable,
		"ErrNoWebhookSecret":       ErrNoWebhookSecret,
		"ErrInstallationShared":    ErrInstallationShared,
		"ErrAppNotFound":           ErrAppNotFound,
		"ErrAppAlreadyConnected":   ErrAppAlreadyConnected,
		"ErrCredentialNotFound":    ErrCredentialNotFound,
		"ErrBindingTaken":          ErrBindingTaken,
		"ErrInstallationTaken":     ErrInstallationTaken,
		"ErrInvalidBinding":        ErrInvalidBinding,
	}, map[string]string{
		"ErrInvalidKey":          "always wrapped in ErrInvalidInput before it reaches a handler",
		"ErrRepoNotCovered":      "raised by token minting for the agents, not by a credential handler",
		"ErrNoWebhookSecret":     "handled inside the webhook service, which mints a secret",
		"ErrAppNotFound":         "a store rule; the per-App service that surfaces it maps it",
		"ErrAppAlreadyConnected": "a store rule; the per-App service that surfaces it maps it",
		"ErrCredentialNotFound":  "a store rule; the per-App service that surfaces it maps it",
		"ErrBindingTaken":        "a store rule; the per-App service that surfaces it maps it",
		"ErrInstallationTaken":   "a store rule; the per-App service that surfaces it maps it",
		"ErrInvalidBinding":      "a store rule; the per-App service that surfaces it maps it",
	})
}
