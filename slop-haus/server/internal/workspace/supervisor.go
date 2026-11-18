package workspace

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode"
)

//go:embed assets/sh/supervisor-setup.sh
var supervisorSetupBody string

// SupervisorParams is what a supervisor setup script is made for.
type SupervisorParams struct {
	// Server is where the script's reader reached slop-haus.
	Server string
	// TenantID is the tenant whose workers the supervisor will run; the
	// supervisor records it so renaming the tenant's directory never orphans it.
	TenantID string
	// TenantName is the tenant's name as its owner typed it; the script turns it
	// into the folder name (see SlugTenantName). May be empty.
	TenantName string
	// Archive is the supervisor package as the server ships it.
	Archive []byte
}

// SlugTenantName is the name a tenant's folder and supervisor go by on a
// machine: lowercase letters, digits and single dashes. A name with none (all
// symbols, say) gives "", and the script falls back to the server's host label.
func SlugTenantName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r < unicode.MaxASCII && (unicode.IsLower(r) || unicode.IsDigit(r)) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	return b.String()
}

// SupervisorScript renders the script that sets up a supervisor root on a
// machine: it checks for uv, resolves the root and unpacks the package into it.
//
// Like the workspace setup script it carries no credential. The package is in
// it instead of fetched, because no agent exists yet to fetch it as.
func SupervisorScript(p SupervisorParams) (string, error) {
	// Both end up inside single quotes in the script, as data.
	for name, v := range map[string]string{"server": p.Server, "tenant id": p.TenantID} {
		if v == "" || strings.ContainsAny(v, "\r\n") {
			return "", fmt.Errorf("supervisor setup needs a %s on one line", name)
		}
	}
	if len(p.Archive) == 0 {
		return "", fmt.Errorf("supervisor setup needs the package archive")
	}

	var b strings.Builder
	fmt.Fprintf(&b, `#!/usr/bin/env bash
# slop-haus supervisor setup.
#
# Sets up the directory a slop-haus supervisor runs from on this machine:
# checks that uv is installed, then writes supervisor.toml, secrets.env and the
# harness directories, and unpacks the supervisor into app/. Run it again to
# update; slots and secrets already there are kept.
#
# This file carries no credential: it names the server and the tenant, and the
# supervisor package.
set -euo pipefail

server=%s
tenant_id=%s
tenant_name=%s
archive_b64=$(cat <<'SLOPHAUS_ARCHIVE'
`, ShellQuote(strings.TrimSuffix(p.Server, "/")), ShellQuote(p.TenantID), ShellQuote(SlugTenantName(p.TenantName)))

	encoded := base64.StdEncoding.EncodeToString(p.Archive)
	for len(encoded) > 76 {
		b.WriteString(encoded[:76] + "\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded + "\nSLOPHAUS_ARCHIVE\n)\n\n")
	b.WriteString(supervisorSetupBody)
	return b.String(), nil
}
