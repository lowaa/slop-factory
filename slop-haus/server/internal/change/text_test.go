package change

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPersonName_IsOneLineOfPlainText(t *testing.T) {
	for raw, want := range map[string]string{
		"Ada Lovelace":                 "Ada Lovelace",
		"  Ada\n\tLovelace  ":          "Ada Lovelace",
		"@everyone <b>hi</b>":          "everyone b hi b",
		"[x](https://evil.example)":    "x (https evil.example)",
		"Zoë O'Brien-Smith (ops), Jr.": "Zoë O'Brien-Smith (ops), Jr.",
		"":                             "",
	} {
		assert.Equal(t, want, personName(raw), "%q", raw)
	}
	assert.Len(t, []rune(personName(strings.Repeat("a", 500))), maxNameRunes)
}

func TestSingleLine_DropsControlCharacters(t *testing.T) {
	assert.Equal(t, "Add the thing now", singleLine("Add the\nthing\r\n\x00 now"))
	assert.Len(t, []rune(singleLine(strings.Repeat("é", 500))), maxSubjectRunes)
}

func TestFooter_NamesWhoIsKnown(t *testing.T) {
	got := footer(12, "https://board.example/tickets/12", &People{Agent: "builder-1", Approver: "Alex", ProposalApprovers: []string{"Alex (ready)", "Sam (status)"}})

	assert.Equal(t, "---\nslop-haus #12: https://board.example/tickets/12\nAgent: builder-1\nApproved by: Alex\nProposals approved by: Alex (ready), Sam (status)\n", got)
	assert.Equal(t, "---\nslop-haus #3\n", footer(3, "", &People{}), "no link, no people: just the ticket")
}

func TestValidBranch(t *testing.T) {
	for _, ok := range []string{"ticket-298-x", "feature/a_b.c", "a@b"} {
		assert.NoError(t, validBranch(ok), ok)
	}
	for _, bad := range []string{"", "-x", "/x", "x/", "x.lock", "x.", "refs/heads/x", "a..b", "a//b", "a@{0}", "@", "a b", "a~1", "a^", "a:b", "a?b", "a*b", "a[b", "a\\b", "a\x01b", "feature/.hidden", "feature/name.", "feature/name.lock/x", "a/./b"} {
		assert.ErrorIs(t, validBranch(bad), ErrInvalid, "%q", bad)
	}
}
