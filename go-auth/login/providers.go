package login

import "fmt"

// Providers are the login providers a service is configured with, in the
// order its sign-in page offers them. Only their identities can sign in,
// so only they count as a way into an account.
type Providers struct {
	list   []AuthProvider
	byName map[string]AuthProvider
}

// NewProviders panics on two providers with one name: a name is an
// identity namespace, and a sign-in started with one provider would be
// finished by the other. The configuration is the service's own, so that
// is a bug to find at startup.
func NewProviders(providers ...AuthProvider) *Providers {
	p := &Providers{byName: map[string]AuthProvider{}}
	for _, ap := range providers {
		if _, taken := p.byName[ap.Name()]; taken {
			panic(fmt.Sprintf("login: two providers are named %q", ap.Name()))
		}
		p.list = append(p.list, ap)
		p.byName[ap.Name()] = ap
	}
	return p
}

// Get is the provider named name, if it is configured.
func (p *Providers) Get(name string) (AuthProvider, bool) {
	if p == nil {
		return nil, false
	}
	ap, ok := p.byName[name]
	return ap, ok
}

// List is every configured provider.
func (p *Providers) List() []AuthProvider {
	if p == nil {
		return nil
	}
	return p.list
}

// Names is every configured provider's name.
func (p *Providers) Names() []string {
	names := make([]string, 0, len(p.List()))
	for _, ap := range p.List() {
		names = append(names, ap.Name())
	}
	return names
}
