// Application-specific wrapper over the raw browser navigations this app
// makes outside its router --
// the same reasoning api/index.ts wraps fetch and EventSource with. It also gives tests something to spy on directly:
// jsdom's window.location.assign is a non-configurable own property, so
// vi.spyOn(window.location, "assign") throws "Cannot redefine property" --
// spying on this module's export instead sidesteps that entirely.

// reloadToBoard is what every action that moves the signed-in session into
// a different tenant ends with -- accepting an invite (invite_page.tsx) and
// switching tenants (tenant_switcher.tsx). A full navigation rather than an
// in-place AuthContext update: the new tenant's tickets, agents and members
// have never been fetched in this tab, so reloading is simpler and more
// correct than hand-invalidating every provider's cache for a transition
// that happens rarely, not on every render.
export function reloadToBoard(): void {
  window.location.assign("/");
}

// reloadPage is the same full navigation for a tenant switch that should
// land back where it started: a ticket link that named another tenant is
// still the page to show once the session is in that tenant.
export function reloadPage(): void {
  window.location.reload();
}

// redirectBrowserTo is a full navigation to an arbitrary URL -- what the
// consent and device pages end an approval or denial with (oauth.go's
// RedirectResponse). It has to be a real navigation rather than
// pushState/fetch: the target is a loopback URL on the user's own machine
// (an MCP client's callback server), not a route this SPA owns, and OAuth's
// code/error land in its query string the way any redirect delivers one.
// Sending the browser to a login provider -- a sign-in, an invite, a link
// or a step-up -- is the same kind of navigation, to the provider's site.
export function redirectBrowserTo(url: string): void {
  window.location.assign(url);
}

// postFormTo navigates the browser to url with fields as a form POST -- what
// GitHub's create-an-App-from-a-manifest page takes: the manifest is a form
// field, not a query. A real form submission, as redirectBrowserTo is a real
// navigation: the target is GitHub's page, not a route of this SPA.
export function postFormTo(url: string, fields: Record<string, string>): void {
  const form = document.createElement("form");
  form.method = "POST";
  form.action = url;
  for (const [name, value] of Object.entries(fields)) {
    const input = document.createElement("input");
    input.type = "hidden";
    input.name = name;
    input.value = value;
    form.appendChild(input);
  }
  document.body.appendChild(form);
  form.submit();
}
