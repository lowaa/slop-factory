package legaldocs_test

import (
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-privacy/consent/consenttest"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs/schema"
)

// The consent tables come too, for the test that drives a consent Service
// from these documents.
func migrations() fs.FS {
	out := fstest.MapFS{"000003_legaldocs.sql": {Data: []byte(schema.LegalDocs)}}
	src := consenttest.Migrations()
	for _, name := range []string{"000001_global_users.sql", "000002_consent.sql"} {
		data, err := fs.ReadFile(src, name)
		if err != nil {
			panic(err)
		}
		out[name] = &fstest.MapFile{Data: data}
	}
	return out
}

func TestMain(m *testing.M) {
	dbtest.Main(m, migrations())
}

var (
	jan1 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	jun1 = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }
