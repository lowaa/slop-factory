package fleetinstructions_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
)

func TestMatch(t *testing.T) {
	list := "secret\n  api key  \n\n\nprod\nreal user\n"
	tests := map[string]struct {
		list, text string
		word       string
		ok         bool
	}{
		"plain word":             {list, "rotate the secret", "secret", true},
		"case insensitive":       {list, "ROTATE THE Secret now", "secret", true},
		"entry case is ignored":  {"SECRET", "a secret", "SECRET", true},
		"whole words only":       {list, "ship the product", "", false},
		"word inside a longer":   {list, "secretary", "", false},
		"underscore joins":       {list, "prod_db", "", false},
		"punctuation bounds":     {list, "(prod), please", "prod", true},
		"start and end of text":  {list, "prod", "prod", true},
		"phrase":                 {list, "need an API key today", "api key", true},
		"phrase across spaces":   {list, "need an api   key", "api key", true},
		"phrase across newline":  {list, "need an api\nkey", "api key", true},
		"half a phrase":          {list, "the key to the api", "", false},
		"surrounding spaces":     {list, "a real user wrote", "real user", true},
		"blank lines never hit":  {"\n \n\t\n", "anything at all", "", false},
		"empty list":             {"", "secret prod", "", false},
		"regexp characters":      {"a.b\nc++", "axb and c++ here", "c++", true},
		"dot is literal":         {"a.b", "axb", "", false},
		"first listed entry won": {"prod\nsecret", "secret in prod", "prod", true},
		"non ascii letters":      {"café", "the café opens", "café", true},
		"non ascii neighbour":    {"cafe", "cafés", "", false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			word, ok := fleetinstructions.Match(tt.list, tt.text)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.word, word)
		})
	}
}
