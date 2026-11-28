//go:build !linux

package scripted

import "net"

// track and endLaunched need peer credentials and pidfds (proc_linux.go).
// Elsewhere a TUI harness may outlive its test by the moment it takes to
// exit.
func (p *Puppet) track(net.Conn, int) {}

func (p *Puppet) endLaunched() {}
