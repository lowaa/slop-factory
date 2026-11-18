package main

import (
	"context"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/onboarding"
)

type onboardingModule struct {
	Handler *onboarding.Handler
}

// newOnboardingModule binds the claim's setup check to the onboarding
// service, unless the gate is switched off.
func newOnboardingModule(d *moduleDeps) (module, error) {
	c, s := d.container, d.svcs
	ws, ch := lookup[*workspaceModule](d.built), lookup[*codeHostModule](d.built)
	svc := onboarding.NewService(ws.Service, ch.Credentials)
	if c.cfg.SetupClaimGate {
		s.setupCheck.Bind(svc)
	} else {
		s.setupCheck.Bind(setupGateOff{})
	}
	return &onboardingModule{Handler: onboarding.NewHandler(svc, c.logHandler)}, nil
}

// setupGateOff is the claim's setup check with SETUP_CLAIM_GATE off.
type setupGateOff struct{}

func (setupGateOff) SetupGap(context.Context) string { return "" }

func (m *onboardingModule) Routes(r *apiserver.Deps) { r.OnboardingHandler = m.Handler }

func (*onboardingModule) Close() {}
