package apiserver

import "github.com/danielgtaylor/huma/v2"

// withoutErrorValues takes the offending values out of huma's validation
// errors. huma quotes each one back: the field's value, the whole object for
// a missing or unexpected property, and the raw body for one that does not
// parse. A request body is where passwords, tokens and private keys arrive,
// and an answer that repeats them puts them in front of every proxy, browser
// tool and error report that sees it. The message and location still say
// what was wrong and where, which is all a client needs to fix the request.
func withoutErrorValues(_ huma.Context, _ string, v any) (any, error) {
	if m, ok := v.(*huma.ErrorModel); ok {
		for _, d := range m.Errors {
			if d != nil {
				d.Value = nil
			}
		}
	}
	return v, nil
}
