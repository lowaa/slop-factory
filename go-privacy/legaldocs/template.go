package legaldocs

import (
	"embed"
	"fmt"
	"time"
)

//go:embed templates/*.md
var templates embed.FS

// Template returns a new version file for a built-in kind: front matter
// with reviewed: false, then section headings and TODO-LEGAL markers. It is
// where every document starts. Material is true, the safe choice for a
// first version: nobody has agreed to anything yet, and the author decides
// when a later one is not.
//
// It carries no legal text: the sections are slots for text that someone
// qualified has reviewed.
func Template(kind, version string, effective time.Time) ([]byte, error) {
	body, err := templates.ReadFile("templates/" + kind + ".md")
	if err != nil {
		return nil, fmt.Errorf("legaldocs: no template for kind %q", kind)
	}
	head := fmt.Sprintf("---\nkind: %s\nversion: %s\neffective: %s\nlocale: en\nmaterial: true\nreviewed: false\n---\n",
		kind, version, effective.Format(time.DateOnly))
	return append([]byte(head), body...), nil
}
