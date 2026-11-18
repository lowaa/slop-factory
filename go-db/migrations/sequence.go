package migrations

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LatestFileName is the file, kept beside the migrations, naming the newest
// one. Every branch that adds a migration edits it, so two branches adding
// "the next" migration conflict in git at merge time, before any test runs.
// goose only loads *.sql and *.go files, so it ignores this one.
const LatestFileName = "LATEST"

var migrationFileRe = regexp.MustCompile(`^(\d{6})_([a-z][a-z0-9_]*)\.(sql|go)$`)

// SequenceLint configures the sequence check run by its Lint method.
type SequenceLint struct {
	// FirstVersion is the lowest version the sequence starts at; 0 means 1.
	// It exists for a history that was restarted deliberately (an applied
	// history cannot be renumbered), and each use should say why.
	FirstVersion int
}

// Lint reports every way the migrations in fsys (rooted directly at the
// migration files) fail to form one sequence: well-formed names, unique
// versions, no gaps from FirstVersion, and a LATEST file naming the highest.
// It needs no database, so it runs as a plain unit test.
func (l *SequenceLint) Lint(fsys fs.FS) []error {
	first := l.FirstVersion
	if first == 0 {
		first = 1
	}

	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return []error{fmt.Errorf("migrations: sequence lint: read dir: %w", err)}
	}

	var errs []error
	byVersion := map[int][]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == LatestFileName {
			continue
		}
		ext := path.Ext(name)
		if ext != ".sql" && ext != ".go" {
			continue
		}
		m := migrationFileRe.FindStringSubmatch(name)
		if m == nil {
			errs = append(errs, fmt.Errorf("migration file %q is malformed: want <6-digit version>_<lower_snake_name>.sql, e.g. 000043_add_widgets.sql", name))
			continue
		}
		v, _ := strconv.Atoi(m[1])
		byVersion[v] = append(byVersion[v], name)
	}

	versions := make([]int, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	sort.Ints(versions)

	for _, v := range versions {
		if names := byVersion[v]; len(names) > 1 {
			sort.Strings(names)
			errs = append(errs, fmt.Errorf("version %d is used by more than one migration (%s): another migration landed with the same number first; renumber yours to follow it and update %s", v, strings.Join(names, ", "), LatestFileName))
		}
	}

	next := first
	for _, v := range versions {
		if v < first {
			errs = append(errs, fmt.Errorf("version %d is below the first version %d of this sequence", v, first))
			continue
		}
		if v > next {
			errs = append(errs, fmt.Errorf("gap in migration versions: %d is missing before %d; renumber to close the gap (versions must be sequential)", next, v))
		}
		next = v + 1
	}

	errs = append(errs, l.lintLatest(fsys, byVersion, versions)...)
	return errs
}

func (l *SequenceLint) lintLatest(fsys fs.FS, byVersion map[int][]string, versions []int) []error {
	raw, err := fs.ReadFile(fsys, LatestFileName)
	if err != nil {
		return []error{fmt.Errorf("%s is missing or unreadable (%w): create it holding the newest migration's name without extension, e.g. 000043_add_widgets", LatestFileName, err)}
	}
	if len(versions) == 0 {
		return []error{errors.New("no migrations found, but " + LatestFileName + " exists")}
	}

	got := strings.TrimSpace(string(raw))
	highest := byVersion[versions[len(versions)-1]]
	sort.Strings(highest)
	for _, f := range highest {
		if got+path.Ext(f) == f {
			return nil
		}
	}
	return []error{fmt.Errorf("%s says %q but the highest migration is %q: set %s to %q (if you merged main and got a conflict here, another migration landed first: renumber yours after theirs, and check yours still makes sense after theirs, not just that it applies)",
		LatestFileName, got, highest[0], LatestFileName, strings.TrimSuffix(highest[0], path.Ext(highest[0])))}
}
