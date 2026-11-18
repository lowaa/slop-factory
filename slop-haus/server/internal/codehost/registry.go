package codehost

import (
	"fmt"
	"strings"
)

// Registry is the code hosts this server knows, by name. It is built once
// at startup and only read after, so it needs no lock.
type Registry struct {
	hosts   []Host
	changes map[string]func() Changes
}

func NewRegistry() *Registry {
	return &Registry{changes: map[string]func() Changes{}}
}

// Register adds a host. changes is nil for a host slop-haus can read links
// and webhooks of but not act on. It panics on a repeated name: a second
// registration is a wiring mistake, and silently shadowing a host would
// send tokens to the wrong one.
func (r *Registry) Register(h Host, changes Changes) {
	if _, ok := r.Host(h.Name()); ok {
		panic(fmt.Sprintf("codehost: host %q registered twice", h.Name()))
	}
	r.hosts = append(r.hosts, h)
	if changes != nil {
		r.changes[strings.ToLower(h.Name())] = func() Changes { return changes }
	}
}

// RegisterLate adds a host whose operations exist only after the registry
// does (they need the credential service, which is built from services that
// hold the registry). changes is called on every read of the host's
// operations, so it must not be called before they are bound; it panics on a
// repeated name like Register.
func (r *Registry) RegisterLate(h Host, changes func() Changes) {
	r.Register(h, nil)
	r.changes[strings.ToLower(h.Name())] = changes
}

// Host is the registered host called name ("github.com").
func (r *Registry) Host(name string) (Host, bool) {
	for _, h := range r.hosts {
		if strings.EqualFold(h.Name(), name) {
			return h, true
		}
	}
	return nil, false
}

// ParseChangeURL reads a change link on whichever host it belongs to.
func (r *Registry) ParseChangeURL(raw string) (ChangeRef, bool) {
	for _, h := range r.hosts {
		if ref, ok := h.ParseChangeURL(raw); ok {
			return ref, true
		}
	}
	return ChangeRef{}, false
}

// ChangeURL is the canonical link of a change on its host, or "" if the
// host is not registered.
func (r *Registry) ChangeURL(ref ChangeRef) string {
	if h, ok := r.Host(ref.Host); ok {
		return h.ChangeURL(ref)
	}
	return ""
}

// ParseRemote reads a manifest remote on whichever host it belongs to,
// returning the host's name with the repository.
func (r *Registry) ParseRemote(remote string) (host string, repo Repo, ok bool) {
	name, _, _ := strings.Cut(remote, "/")
	h, found := r.Host(name)
	if !found {
		return "", Repo{}, false
	}
	repo, ok = h.ParseRemote(remote)
	return h.Name(), repo, ok
}

// Changes is the operations on the named host's changes.
func (r *Registry) Changes(host string) (Changes, bool) {
	c, ok := r.changes[strings.ToLower(host)]
	if !ok {
		return nil, false
	}
	return c(), true
}
