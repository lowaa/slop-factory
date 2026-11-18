package secretbox

import (
	"fmt"
	"log/slog"
)

const redacted = "[redacted]"

// Redacted is embedded by the types that hold a secret, so that none
// of the ways a value ends up in a log line, an error message or a JSON
// body prints it: fmt (%v, %+v, %#v, %s, %q), slog and encoding/json.
type Redacted struct{}

func (Redacted) String() string               { return redacted }
func (Redacted) GoString() string             { return redacted }
func (Redacted) Format(f fmt.State, _ rune)   { _, _ = f.Write([]byte(redacted)) }
func (Redacted) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (Redacted) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }
func (Redacted) MarshalText() ([]byte, error) { return []byte(redacted), nil }
