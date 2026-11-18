package legaldocs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Version is one published version of a document in one locale.
type Version struct {
	Kind    string
	Version string
	Locale  string
	// Effective is a calendar date, held as midnight UTC.
	Effective time.Time
	// Material versions re-prompt everyone who consented to an earlier
	// one (see consent.DocVersion).
	Material bool
	// Reviewed is false for placeholder text. The page shows a DRAFT
	// banner for it.
	Reviewed   bool
	ReviewedBy string
	ReviewedOn *time.Time
	Body       string
	// SourceHash is the SHA-256 of the file the version was loaded from, as
	// written: front matter and {{inventory:...}} directives, before they
	// are filled. It is empty for a Version not read by a Loader, and is not
	// part of ContentHash: what was published is the rendered text.
	SourceHash string
}

// ContentHash identifies the exact content of v: republishing a version is
// allowed only when it is the same.
func (v *Version) ContentHash() string {
	b, _ := json.Marshal(struct {
		Kind, Version, Locale, Effective string
		Material, Reviewed               bool
		ReviewedBy, ReviewedOn, Body     string
	}{v.Kind, v.Version, v.Locale, v.Effective.Format(time.DateOnly), v.Material, v.Reviewed,
		v.ReviewedBy, dateOrEmpty(v.ReviewedOn), v.Body})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func dateOrEmpty(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.DateOnly)
}
