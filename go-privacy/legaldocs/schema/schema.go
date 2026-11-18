// Package schema holds the DDL for go-privacy's legal document table. An
// app copies LegalDocs into its own numbered migration, as with the consent
// tables (go-privacy/consent/schema), and runs legaldocs.StoreSuite against
// its migrated database.
package schema

import _ "embed"

//go:embed legaldocs.sql
var LegalDocs string
