// A story `beforeEach` that puts a token in the URL fragment, where the
// token pages read it during their first render (a play function runs too
// late), and restores the exact URL afterwards.
//
// Only the fragment changes: under the Storybook test runner the story
// renders inside the runner's own page, and rewriting its path breaks any
// later reload of that page -- the runner then loads the wrong URL and
// never reconnects.
export function withTokenInUrl(token: string | null) {
  return () => {
    const original = window.location.href;
    const { pathname, search } = window.location;
    window.history.replaceState(null, "", token ? `${pathname}${search}#token=${token}` : `${pathname}${search}`);
    return () => window.history.replaceState(null, "", original);
  };
}
