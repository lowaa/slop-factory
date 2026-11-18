// Module slop-review/report is the contract between slop-review (the
// reviewer) and a report server (not part of this repo): package report defines
// the shape of a run and how to validate one, and package reporter is the
// client that posts a run and retries what it couldn't deliver.
//
// A change here that the server must know about to keep reading reports
// correctly -- a field's meaning changing, not just a field being added --
// bumps report.SchemaVersion; see its doc comment for what that covers.
package report
