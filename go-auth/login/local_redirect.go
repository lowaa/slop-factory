package login

import "strings"

// LocalRedirect returns next unchanged if it is a path on this site, and
// "/" otherwise, so a crafted sign-in link can't be used as an open
// redirect. Anything that could leave the site is refused: an absolute
// URL, a scheme-relative "//evil.example", a backslash (browsers treat it
// as a slash), and a path whose dot segments collapse into a leading "//"
// ("/.//evil.example", "/%2e//evil.example"). A browser resolving the raw
// value would stay on the site, but a consumer that normalises it first
// and then reuses the result as a URL would not.
//
// Control characters and malformed "%" escapes are refused anywhere in
// the value: browsers strip the former ("/\t/evil.example" is
// "//evil.example" to them), and the frontends' safeRedirectPath refuses
// both, which keeps what they accept a subset of what this accepts.
//
// The cases in testdata/local_redirect_cases.json drive both this
// function's tests and safeRedirectPath's (js-components/router), and
// those check that every path safeRedirectPath accepts is accepted here
// too and leads to the same place.
func LocalRedirect(next string) string {
	if !isLocalPath(next) {
		return "/"
	}
	return next
}

func isLocalPath(next string) bool {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, `\`) {
		return false
	}
	if hasControlChar(next) || hasMalformedEscape(next) {
		return false
	}
	// No url.Parse: past the checks above it can't fail, and a value that
	// starts with a single "/" has no scheme or host to it. Each check here
	// is one the case file exercises on its own.
	return !strings.HasPrefix(removeDotSegments(pathOf(next)), "//")
}

func hasControlChar(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// hasMalformedEscape reports a "%" not followed by two hex digits.
func hasMalformedEscape(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && (i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2])) {
			return true
		}
	}
	return false
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// pathOf returns the path of a URL that has no scheme or authority.
func pathOf(ref string) string {
	if i := strings.IndexAny(ref, "?#"); i >= 0 {
		return ref[:i]
	}
	return ref
}

// removeDotSegments resolves "." and ".." segments of an absolute path the
// way the WHATWG URL parser -- so every browser, and safeRedirectPath --
// does. Unlike path.Clean it keeps empty segments: "/a//b" is not "/a/b"
// to a browser, and an empty first segment is exactly what must be found.
// It also treats "%2e" as a dot, as browsers do.
func removeDotSegments(p string) string {
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	out := make([]string, 0, len(segs))
	for i, seg := range segs {
		last := i == len(segs)-1
		switch {
		case isDoubleDotSegment(seg):
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		case isSingleDotSegment(seg):
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, seg)
		}
	}
	return "/" + strings.Join(out, "/")
}

func isSingleDotSegment(seg string) bool {
	return seg == "." || strings.EqualFold(seg, "%2e")
}

func isDoubleDotSegment(seg string) bool {
	switch strings.ToLower(seg) {
	case "..", ".%2e", "%2e.", "%2e%2e":
		return true
	}
	return false
}
