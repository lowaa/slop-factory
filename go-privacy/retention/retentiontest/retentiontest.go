// Package retentiontest holds the schema retention's tests run against.
package retentiontest

import (
	_ "embed"
	"io/fs"
	"testing/fstest"
)

//go:embed schema.sql
var schema string

func Migrations() fs.FS {
	return fstest.MapFS{"000001_things.sql": {Data: []byte(schema)}}
}
