// The one full-page navigation the package makes: to a login provider, or
// to a URL a server answered a round-trip start with. A wrapper rather than
// window.location.assign at each call site so tests and stories can swap
// it out (jsdom's location.assign can't be spied on) -- apps pass their own
// through AuthPagesConfig.redirect / the panel's redirect prop.
export function redirectTo(url: string): void {
  window.location.assign(url);
}
