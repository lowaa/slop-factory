package service

import (
	"bytes"
	"embed"
	"encoding/xml"
	"fmt"
	"strings"
	"text/template"
	"unicode"
)

//go:embed slop-bot.service.tmpl slop-bot.plist.tmpl
var templateFS embed.FS

var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"execPath":   systemdExecPath,
	"execArg":    systemdExecArg,
	"specifiers": systemdSpecifiers,
	"envAssign":  systemdEnv,
	"xml":        xmlText,
}).ParseFS(templateFS, "*.tmpl"))

// render runs the named template. Escaping errors (a control character in a
// path) surface here rather than as a unit that loads differently from what
// was resolved.
func render(name string, s *spec) ([]byte, error) {
	values := append([]string{s.Binary, s.WorkingDir, s.LogFile}, s.Args...)
	for _, v := range append(values, envStrings(s.Env)...) {
		if strings.ContainsFunc(v, unicode.IsControl) {
			return nil, fmt.Errorf("service: %q contains a control character, which a unit file can't carry", v)
		}
	}
	var b bytes.Buffer
	if err := templates.ExecuteTemplate(&b, name, s); err != nil {
		return nil, fmt.Errorf("service: rendering %s: %w", name, err)
	}
	return b.Bytes(), nil
}

func envStrings(env []EnvVar) []string {
	out := make([]string, 0, 2*len(env))
	for _, e := range env {
		out = append(out, e.Name, e.Value)
	}
	return out
}

// systemdSpecifiers escapes systemd's %-specifiers, which it expands in
// nearly every setting: a literal % in a path would otherwise become %h or
// similar.
func systemdSpecifiers(v string) string { return strings.ReplaceAll(v, "%", "%%") }

// systemdQuote double-quotes v with the C-style escapes systemd's quoted
// strings take.
func systemdQuote(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}

// systemdExecPath is the ExecStart executable: quoted so a space doesn't
// split it. systemd expands $ only in the arguments after it, so a $ here
// stays as it is (verified with systemd-analyze).
func systemdExecPath(v string) string {
	return systemdQuote(systemdSpecifiers(v))
}

// systemdExecArg is an ExecStart argument, where $VAR is expanded, so a
// literal $ is doubled.
func systemdExecArg(v string) string {
	return systemdQuote(strings.ReplaceAll(systemdSpecifiers(v), "$", "$$"))
}

// systemdEnv is an Environment= assignment. Environment= expands
// specifiers but not $.
func systemdEnv(e EnvVar) string { return systemdQuote(systemdSpecifiers(e.Name + "=" + e.Value)) }

func xmlText(v string) (string, error) {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(v)); err != nil {
		return "", err
	}
	return b.String(), nil
}
