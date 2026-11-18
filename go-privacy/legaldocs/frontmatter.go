package legaldocs

import (
	"bytes"
	"errors"
	"time"

	"go.yaml.in/yaml/v3"
)

// frontMatter is the file header (go-privacy/README.md §10). Material is a
// pointer so a file that leaves it out can be told from one that says false:
// the decision whether a version re-prompts is the publisher's, with no
// default to fall back on.
type frontMatter struct {
	Kind       string     `yaml:"kind"`
	Version    string     `yaml:"version"`
	Effective  *time.Time `yaml:"effective"`
	Locale     string     `yaml:"locale"`
	Material   *bool      `yaml:"material"`
	Reviewed   bool       `yaml:"reviewed"`
	ReviewedBy string     `yaml:"reviewed_by"`
	ReviewedOn *time.Time `yaml:"reviewed_on"`
}

var fence = []byte("---\n")

// splitFrontMatter returns the header and the Markdown after it.
func splitFrontMatter(data []byte) (*frontMatter, string, error) {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(data, fence) {
		return nil, "", errors.New("missing front matter")
	}
	rest := data[len(fence):]
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		if !bytes.HasSuffix(rest, []byte("\n---")) {
			return nil, "", errors.New("front matter is not closed")
		}
		end = len(rest) - len("\n---")
		rest = append(rest[:end:end], []byte("\n---\n")...)
	}
	header, body := rest[:end], rest[end+len("\n---\n"):]

	fm := &frontMatter{}
	dec := yaml.NewDecoder(bytes.NewReader(header))
	// An unknown key is a typo (e.g. "materiel:") that would otherwise be
	// dropped silently.
	dec.KnownFields(true)
	if err := dec.Decode(fm); err != nil {
		return nil, "", errors.New("front matter is not valid: " + err.Error())
	}
	return fm, string(body), nil
}
