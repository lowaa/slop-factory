package seeddata

// Note declares what a migration legitimately does to rows that were already
// there. Anything else it does to existing rows is a finding: Compare fails
// on a row that vanished or changed, on a column that went away and on a
// table that was dropped.
type Note struct {
	// Why the migration may touch existing data, for the next reader.
	Why string
	// DroppedTables may disappear, with their rows.
	DroppedTables []string
	// RewrittenColumns may change value or disappear, per table.
	RewrittenColumns map[string][]string
	// RenamedColumns maps a table's old column name to its new one; the values
	// must carry over.
	RenamedColumns map[string]map[string]string
	// RewrittenTables may lose, gain or change rows outright.
	RewrittenTables []string
}

// Notes are the migrations that may touch existing data, by version. One
// absent from here must leave every existing row exactly as it was.
var Notes = map[int64]Note{
	176: {
		Why:             "codehost_credentials is split into codehost_apps and a new codehost_credentials (one credential per migrated row); the installation lookup is rebuilt by the triggers",
		RewrittenTables: []string{"codehost_credentials", "global_github_app_installations"},
	},
}

// NotesBetween merges the notes of the migrations after from, up to and
// including to: what a database may have lost going from one version to
// the other. Two migrations renaming the same column in turn are not
// chained; none do.
func NotesBetween(from, to int64) Note {
	merged := Note{RenamedColumns: map[string]map[string]string{}, RewrittenColumns: map[string][]string{}}
	for version, n := range Notes {
		if version <= from || version > to {
			continue
		}
		merged.DroppedTables = append(merged.DroppedTables, n.DroppedTables...)
		merged.RewrittenTables = append(merged.RewrittenTables, n.RewrittenTables...)
		for table, cols := range n.RewrittenColumns {
			merged.RewrittenColumns[table] = append(merged.RewrittenColumns[table], cols...)
		}
		for table, renames := range n.RenamedColumns {
			if merged.RenamedColumns[table] == nil {
				merged.RenamedColumns[table] = map[string]string{}
			}
			for old, renamed := range renames {
				merged.RenamedColumns[table][old] = renamed
			}
		}
	}
	return merged
}
