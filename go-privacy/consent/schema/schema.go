// Package schema holds the DDL for go-privacy's consent tables.
//
// An app does not run it from here: go-db takes one migration source per
// database, so the app copies Consent into its own numbered migration (the
// file is already in goose's format) and runs consenttest.StoreSuite
// against its migrated database, which fails if the copy drifts from the
// SQL the store runs.
package schema

import _ "embed"

//go:embed consent.sql
var Consent string
