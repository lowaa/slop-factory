package engine

import (
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithCredentials_HappyPath(t *testing.T) {
	got, err := WithCredentials("postgres://admin:secret@db:5432/app?sslmode=disable&pool_max_conns=99",
		&DSNCredentials{User: "app", Password: "p@ss/w:rd?#% ü", MaxConns: 7})
	require.NoError(t, err)

	u, err := url.Parse(got)
	require.NoError(t, err)
	assert.Equal(t, "app", u.User.Username())
	pw, _ := u.User.Password()
	assert.Equal(t, "p@ss/w:rd?#% ü", pw, "special characters are escaped, so they survive a round trip")
	assert.Equal(t, "db:5432", u.Host)
	assert.Equal(t, "/app", u.Path)
	assert.Equal(t, "7", u.Query().Get("pool_max_conns"), "the binary's own size wins over one in DATABASE_URL")
	assert.Equal(t, "disable", u.Query().Get("sslmode"), "the rest of the query is kept")
}

func TestWithCredentials_ZeroMaxConnsLeavesPoolSizeToDSN(t *testing.T) {
	got, err := WithCredentials("postgresql://admin:secret@db/app?pool_max_conns=99", &DSNCredentials{User: "app", Password: "pw"})
	require.NoError(t, err)
	u, err := url.Parse(got)
	require.NoError(t, err)
	assert.Equal(t, "99", u.Query().Get("pool_max_conns"))
	assert.Equal(t, "postgresql", u.Scheme)
}

func TestWithCredentials_NilCredentialsIsAnError(t *testing.T) {
	_, err := WithCredentials("postgres://db/x", nil)
	assert.Same(t, ErrDSNEmptyUser, err)
}

func TestWithCredentials_ErrorsNeverCarryTheDSN(t *testing.T) {
	const (
		ownerPW  = "s3cretOwnerPw"
		appPW    = "s3cretAppPw"
		hostName = "dbhost.internal"
		dbName   = "mydbname"
		ownerUsr = "ownerusr"
	)
	secrets := []string{ownerPW, appPW, hostName, dbName, ownerUsr}
	tests := []struct {
		name string
		dsn  string
		user string
		want error
	}{
		{"control char", "postgres://ownerusr:s3cretOwnerPw@dbhost.internal:5432/mydbname\x7f", "app", ErrDSNInvalid},
		{"bad port", "postgres://ownerusr:s3cretOwnerPw@dbhost.internal:notaport/mydbname", "app", ErrDSNInvalid},
		{"bad escape", "postgres://ownerusr:s3cretOwnerPw@dbhost.internal/mydbname%zz", "app", ErrDSNInvalid},
		{"unicode control", "postgres://ownerusr:s3cretOwnerPw@dbhost.internal/mydbname\u0085\n", "app", ErrDSNInvalid},
		{"keyword value", "host=dbhost.internal user=ownerusr password=s3cretOwnerPw dbname=mydbname", "app", ErrDSNNotURL},
		{"missing scheme", "ownerusr:s3cretOwnerPw@dbhost.internal:5432/mydbname", "app", ErrDSNNotURL},
		{"wrong scheme", "mysql://ownerusr:s3cretOwnerPw@dbhost.internal/mydbname", "app", ErrDSNNotURL},
		{"empty", "", "app", ErrDSNNotURL},
		{"user in query", "postgres://dbhost.internal/mydbname?user=ownerusr&password=s3cretOwnerPw", "app", ErrDSNCredsInQuery},
		{"password in query", "postgres://ownerusr@dbhost.internal/mydbname?sslmode=disable&password=s3cretOwnerPw", "app", ErrDSNCredsInQuery},
		{"bad query escape", "postgres://ownerusr@dbhost.internal/mydbname?application_name=%zz", "app", ErrDSNInvalid},
		{"semicolon in query", "postgres://ownerusr@dbhost.internal/mydbname?a=1;b=2", "app", ErrDSNInvalid},
		{"empty user", "postgres://ownerusr:s3cretOwnerPw@dbhost.internal/mydbname", "", ErrDSNEmptyUser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := WithCredentials(tt.dsn, &DSNCredentials{User: tt.user, Password: appPW})
			require.Error(t, err)
			assert.Same(t, tt.want, err, "the error is the fixed sentinel, nothing derived from the DSN")
			for _, s := range secrets {
				assert.NotContains(t, err.Error(), s)
			}
			assert.Nil(t, errors.Unwrap(err), "a wrapped url.Error would quote the DSN")
		})
	}
}
