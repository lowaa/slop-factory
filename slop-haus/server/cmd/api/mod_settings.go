package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usersettings"
)

// settingsModule is the tenant's settings endpoints. The service stays in
// newServices: the ticket service is gated on it.
type settingsModule struct {
	Handler *settings.Handler
}

func newSettingsModule(d *moduleDeps) (module, error) {
	return &settingsModule{Handler: settings.NewHandler(d.svcs.settings, d.logHandler)}, nil
}

func (m *settingsModule) Routes(r *apiserver.Deps) { r.SettingsHandler = m.Handler }

func (*settingsModule) Close() {}

// userSettingsModule is the per-user and per-user-per-tenant settings
// endpoints.
type userSettingsModule struct {
	Handler       *usersettings.Handler
	TenantHandler *usersettings.TenantHandler
}

func newUserSettingsModule(d *moduleDeps) (module, error) {
	return &userSettingsModule{
		Handler:       usersettings.NewHandler(d.svcs.userSettings, d.logHandler),
		TenantHandler: usersettings.NewTenantHandler(d.svcs.userTenantSettings, d.logHandler),
	}, nil
}

func (m *userSettingsModule) Routes(r *apiserver.Deps) {
	r.UserSettingsHandler = m.Handler
	r.UserTenantSettingsHandler = m.TenantHandler
}

func (*userSettingsModule) Close() {}
