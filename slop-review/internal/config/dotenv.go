package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
)

// dotEnvPrefix is the only prefix read from a .env file: the file may hold
// other projects' secrets, which slop-review has no business holding.
const dotEnvPrefix = "SLOP_REVIEW_"

// dotEnvKey is a shell variable name, optionally after `export `.
var dotEnvKey = regexp.MustCompile(`^(?:export[ \t]+)?([A-Za-z_][A-Za-z0-9_]*)[ \t]*=`)

// DotEnv is the SLOP_REVIEW_* settings of a .env file, by name. It sits
// below the environment: a variable set (non-empty) in the environment
// beats the file's, as an empty one would lose to viper's defaults.
type DotEnv map[string]string

// ReadDotEnv reads path's SLOP_REVIEW_* settings. A missing file, or an
// empty path, is no settings rather than an error: the file is optional.
func ReadDotEnv(path string) (DotEnv, error) {
	if path == "" {
		return DotEnv{}, nil
	}
	f, err := os.Open(path) // #nosec G304 -- the .env the person running slop-review names
	if errors.Is(err, fs.ErrNotExist) {
		return DotEnv{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	d, err := ParseDotEnv(f)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return d, nil
}

// ParseDotEnv reads KEY=VALUE lines, keeping the SLOP_REVIEW_* ones. Blank
// lines and #-comments are skipped, as is a line that isn't an assignment
// at all (another tool's syntax, or the inside of its multi-line value):
// only a SLOP_REVIEW_* line has to parse. A value may be quoted with ' or ",
// taken literally (no escapes, no ${VAR} expansion); unquoted, it ends at a
// " #" comment. The file is never executed. Errors name the line, never a
// value, since a malformed line may be half a secret.
func ParseDotEnv(r io.Reader) (DotEnv, error) {
	d := DotEnv{}
	sc := bufio.NewScanner(r)
	// Room for another tool's long value (a PEM key on one line, say),
	// which would otherwise fail the read.
	sc.Buffer(nil, 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		m := dotEnvKey.FindStringSubmatchIndex(line)
		if m == nil {
			if strings.HasPrefix(strings.TrimPrefix(line, "export "), dotEnvPrefix) {
				return nil, fmt.Errorf("line %d: not a KEY=VALUE assignment", n)
			}
			continue
		}
		key := line[m[2]:m[3]]
		if !strings.HasPrefix(key, dotEnvPrefix) {
			continue
		}
		value, err := dotEnvValue(line[m[1]:])
		if err != nil {
			return nil, fmt.Errorf("line %d (%s): %w", n, key, err)
		}
		d[key] = value
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return d, nil
}

func dotEnvValue(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if q := raw[0]; q == '"' || q == '\'' {
		end := strings.IndexByte(raw[1:], q)
		if end < 0 {
			return "", errors.New("unterminated quote")
		}
		value, rest := raw[1:1+end], strings.TrimSpace(raw[2+end:])
		if rest != "" && !strings.HasPrefix(rest, "#") {
			return "", errors.New("text after the closing quote")
		}
		return value, nil
	}
	if i := strings.Index(raw, " #"); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.Index(raw, "\t#"); i >= 0 {
		raw = raw[:i]
	}
	return strings.TrimSpace(raw), nil
}

// Getenv is the environment's key, or the file's when the environment has
// none (or an empty one).
func (d DotEnv) Getenv(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return d[key]
}

// usedKeys are the file's keys the environment doesn't override, sorted:
// what a run actually took from it.
func (d DotEnv) usedKeys() []string {
	var keys []string
	for _, k := range slices.Sorted(maps.Keys(d)) {
		if os.Getenv(k) == "" {
			keys = append(keys, k)
		}
	}
	return keys
}
