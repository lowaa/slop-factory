package main

import (
	"context"
	"log/slog"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
)

// Every module is built whole: a field left nil would show only at the first
// request that reaches it.
func TestNewModulesBuildsEveryModuleWhole(t *testing.T) {
	t.Parallel()
	role, password := dbtest.AppCredentials()
	cfg, err := config.Load([]string{
		"--database-url=postgres://unused",
		"--app-db-role=" + role,
		"--app-db-password=" + password,
		"--public-base-url=http://127.0.0.1",
		"--jwt-secret=" + bootJWTSecret,
		"--email-backend=log",
	})
	require.NoError(t, err)
	database := dbtest.WithDB(t)
	logs := slog.DiscardHandler
	telemetry := metrics.New(&metrics.Options{Actions: audit.Actions(), Methods: signInMethods(cfg)})
	events := audit.New(logs, telemetry)
	limits := &limiters{telemetry: telemetry, logHandler: logs}
	cl := &closers{}
	t.Cleanup(cl.close)
	late := newUserLate()
	users, err := newUsers(context.Background(), &usersDeps{cfg: cfg, database: database, events: events, limits: limits, late: late}, cl, logs)
	require.NoError(t, err)
	c, err := newContainer(cfg, database, events, limits, users, late, logs)
	require.NoError(t, err)
	svcs, err := newServices(context.Background(), c, &serveSeams{})
	require.NoError(t, err)

	require.Len(t, svcs.modules.list, len(moduleBuilders))
	for _, mod := range svcs.modules.list {
		assertNoNilFields(t, reflect.TypeOf(mod).Elem().Name(), reflect.ValueOf(mod))
	}
}

func assertNoNilFields(t *testing.T, path string, v reflect.Value) {
	t.Helper()
	for v.Kind() == reflect.Pointer {
		require.False(t, v.IsNil(), path)
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct || v.Type().PkgPath() != reflect.TypeFor[modules]().PkgPath() {
		return
	}
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		name := path + "." + v.Type().Field(i).Name
		if f.Kind() == reflect.Pointer || f.Kind() == reflect.Interface {
			assert.False(t, f.IsNil(), name)
		}
		if f.Kind() == reflect.Pointer && !f.IsNil() {
			assertNoNilFields(t, name, f)
		}
	}
}

// A credential key that cannot seal anything stops startup, rather than
// leaving a container that fails on the first credential stored.
func TestNewContainerRefusesAnUnusableCredentialKey(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Credentials: &config.CredentialsConfig{Key: []byte("short")}}
	_, err := newContainer(cfg, dbtest.WithDB(t), nil, nil, nil, newUserLate(), slog.DiscardHandler)
	require.Error(t, err)
}

type recordingModule struct {
	routed, closed bool
}

func (m *recordingModule) Routes(*apiserver.Deps) { m.routed = true }
func (m *recordingModule) Close()                 { m.closed = true }

// A module whose routes or close are not reached by the set would serve
// nothing, or hold a poll open past shutdown, with every other test green.
func TestModulesRouteAndCloseEveryModule(t *testing.T) {
	a, b := &recordingModule{}, &recordingModule{}
	set := &modules{list: []module{a, b}}

	set.Routes(&apiserver.Deps{})
	set.Close()

	for _, m := range []*recordingModule{a, b} {
		assert.True(t, m.routed)
		assert.True(t, m.closed)
	}
}

func TestLookupFindsAModuleByType(t *testing.T) {
	want := &recordingModule{}
	set := &modules{list: []module{&workerModule{}, want}}

	assert.Same(t, want, lookup[*recordingModule](set))
	assert.Panics(t, func() { lookup[*fleetModule](set) })
}
