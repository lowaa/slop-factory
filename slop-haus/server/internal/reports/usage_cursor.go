package reports

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// UsageTicketCursor is the position of the last ticket of a page of the
// per-ticket usage list, which is ranked by tokens and then ticket id. Clients
// only see it encoded, as an opaque string.
type UsageTicketCursor struct {
	// Query pins the cursor to the period and filter it was issued for:
	// replayed against another it would skip or repeat tickets.
	Query    string `json:"q"`
	Tokens   int64  `json:"t"`
	TicketID int64  `json:"i"`
}

func (c *UsageTicketCursor) Encode() string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func DecodeUsageTicketCursor(s string) (*UsageTicketCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: bad cursor", ErrInvalid)
	}
	var c UsageTicketCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.Query == "" || c.Tokens < 0 || c.TicketID <= 0 {
		return nil, fmt.Errorf("%w: bad cursor", ErrInvalid)
	}
	return &c, nil
}

// usageQueryHash names the list a cursor belongs to. The period is its length
// in days, not its bounds: the bounds move with the clock, and a walk that
// straddled a tick should go on, as a keyset walk does when usage arrives.
func usageQueryHash(days int, q *UsageTicketQuery) string {
	return fmt.Sprintf("%d/%t/%s", days, q.IncludeUnreliable, q.Filter.QueryHash())
}
