package secretbox_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
)

type holder struct {
	secretbox.Redacted
	value string
}

func TestRedacted_NoWayOfPrintingAHolderShowsTheValue(t *testing.T) {
	h := &holder{value: "hunter2"}
	for name, out := range map[string]string{
		"%v": fmt.Sprintf("%v", h), "%+v": fmt.Sprintf("%+v", h), "%#v": fmt.Sprintf("%#v", h),
		"%s": fmt.Sprintf("%s", h), "%q": fmt.Sprintf("%q", h), "value %v": fmt.Sprintf("%v", *h),
	} {
		assert.Contains(t, out, "[redacted]", name)
		assert.NotContains(t, out, "hunter2", name)
	}

	raw, err := json.Marshal(map[string]any{"h": h})
	require.NoError(t, err)
	assert.JSONEq(t, `{"h":"[redacted]"}`, string(raw))

	var logs bytes.Buffer
	slog.New(slog.NewJSONHandler(&logs, nil)).Info("x", "h", h)
	assert.Contains(t, logs.String(), "[redacted]")
	assert.NotContains(t, logs.String(), "hunter2")
}
