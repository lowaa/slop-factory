package main

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// userModule is the sign-up and sign-in endpoints. The service is built
// before the container (the agent service needs it for members).
type userModule struct {
	Handler *user.Handler
}

func newUserModule(d *moduleDeps) (module, error) {
	cfg := d.cfg
	return &userModule{Handler: user.NewHandler(d.users, &user.CookieConfig{
		Secure:     cfg.SecureCookies(),
		AccessTTL:  cfg.AccessTokenTTL,
		RefreshTTL: cfg.RefreshTokenTTL,
	}, d.logHandler)}, nil
}

func (m *userModule) Routes(r *apiserver.Deps) { r.UserHandler = m.Handler }

func (*userModule) Close() {}
