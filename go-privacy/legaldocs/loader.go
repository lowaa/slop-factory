package legaldocs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"
)

// maxBody bounds a document, so a stray file can't fill the table or the
// page.
const maxBody = 512 << 10

// LoaderConfig configures a Loader.
type LoaderConfig struct {
	Kinds *Kinds
	// Fragments fills the generated sections; nil leaves them pending.
	Fragments Fragments
}

// Loader reads document versions from files: <kind>/<version>.md under a
// root, each with front matter.
type Loader struct {
	cfg LoaderConfig
}

func NewLoader(cfg *LoaderConfig) (*Loader, error) {
	if cfg.Kinds == nil {
		return nil, errors.New("legaldocs: a loader needs kinds")
	}
	return &Loader{cfg: *cfg}, nil
}

// Load reads every version under root. One bad file fails the whole load:
// an app that starts with a document missing is worse than one that
// doesn't start.
func (l *Loader) Load(fsys fs.FS, root string) ([]*Version, error) {
	var out []*Version
	var errs []error
	seen := map[[3]string]string{}
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".md" {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		v, err := l.Parse(p, data)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			return nil
		}
		key := [3]string{v.Kind, v.Version, v.Locale}
		if other, dup := seen[key]; dup {
			errs = append(errs, fmt.Errorf("%s: same kind, version and locale as %s", p, other))
			return nil
		}
		seen[key] = p
		out = append(out, v)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("legaldocs: read %s: %w", root, err)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("legaldocs: %w", errors.Join(errs...))
	}
	return out, nil
}

// Parse reads one file. The file's path says which kind and version it is
// (<kind>/<version>.md, optionally <kind>/<version>.<locale>.md is not
// used: the locale is front matter), and the front matter must agree, so a
// file copied under a new name can't publish as the old version.
func (l *Loader) Parse(filePath string, data []byte) (*Version, error) {
	fm, md, err := splitFrontMatter(data)
	if err != nil {
		return nil, err
	}
	if !l.cfg.Kinds.Has(fm.Kind) {
		return nil, fmt.Errorf("kind %q is not a declared kind", fm.Kind)
	}
	if !versionPattern.MatchString(fm.Version) {
		return nil, fmt.Errorf("version %q must be letters, digits, dots, dashes and underscores", fm.Version)
	}
	if path.Base(path.Dir(filePath)) != fm.Kind {
		return nil, fmt.Errorf("file is not in a %q directory", fm.Kind)
	}
	if path.Base(filePath) != fm.Version+".md" {
		return nil, fmt.Errorf("file name is not %q", fm.Version+".md")
	}
	if fm.Effective == nil {
		return nil, errors.New("effective is required")
	}
	if fm.Material == nil {
		return nil, errors.New("material is required: say true or false whether this version asks everyone again")
	}
	// No default: a loader's and a service's default could differ, and a
	// version filed under a locale the routes don't serve would be missing.
	if !localePattern.MatchString(fm.Locale) {
		return nil, fmt.Errorf("locale %q is not a locale", fm.Locale)
	}
	locale := fm.Locale
	if len(md) > maxBody {
		return nil, fmt.Errorf("body is over %d bytes", maxBody)
	}
	r, err := render(md, l.cfg.Fragments)
	if err != nil {
		return nil, err
	}
	body := r.body
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("body is empty")
	}
	if fm.Reviewed {
		switch {
		case fm.ReviewedBy == "" || fm.ReviewedOn == nil:
			return nil, errors.New("reviewed: true needs reviewed_by and reviewed_on")
		case strings.Contains(body, todoMarker):
			return nil, errors.New("reviewed: true, but the text still has a TODO-LEGAL marker")
		case r.pending:
			return nil, errors.New("reviewed: true, but a generated section is still pending the inventory")
		case len(r.unresolved) > 0:
			return nil, fmt.Errorf("reviewed: true, but %s/%s has generated sections with unresolved inventory values (%s)",
				fm.Kind, fm.Version, strings.Join(r.unresolved, "; "))
		case len(r.unchecked) > 0:
			return nil, fmt.Errorf("reviewed: true, but %s/%s uses generated sections (%s) from a Fragments that can't say whether they are complete",
				fm.Kind, fm.Version, strings.Join(r.unchecked, ", "))
		}
	}
	source := sha256.Sum256(data)
	v := &Version{
		SourceHash: hex.EncodeToString(source[:]),
		Kind:       fm.Kind, Version: fm.Version, Locale: locale,
		Effective: utcDate(*fm.Effective), Material: *fm.Material,
		Reviewed: fm.Reviewed, ReviewedBy: fm.ReviewedBy, Body: body,
	}
	if fm.ReviewedOn != nil {
		d := utcDate(*fm.ReviewedOn)
		v.ReviewedOn = &d
	}
	if !fm.Reviewed {
		v.ReviewedBy, v.ReviewedOn = "", nil
	}
	return v, nil
}

func utcDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
