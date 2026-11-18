import { useEffect } from "react";

// The tab's title is composed from two independent inputs -- the page's own
// title and the tenant prefix -- so neither has to know about the other and
// the order effects run in (children before parents) does not matter.
const DEFAULT_TITLE = "slop-haus";
const pageTitles: { title: string }[] = [];
let tenantPrefix: string | null = null;

function render() {
  const base = pageTitles.length > 0 ? pageTitles[pageTitles.length - 1].title : DEFAULT_TITLE;
  document.title = tenantPrefix ? `${tenantPrefix} · ${base}` : base;
}

// Sets the browser tab's title while the caller is mounted and falls back to
// the previous page's (or the default) after, so leaving a page does not
// leave its title behind. A null title says nothing (nothing to say yet).
export function useDocumentTitle(title: string | null) {
  useEffect(() => {
    if (title === null) return;
    const entry = { title };
    pageTitles.push(entry);
    render();
    return () => {
      pageTitles.splice(pageTitles.indexOf(entry), 1);
      render();
    };
  }, [title]);
}

// Prefixes every page's title with the tenant's name while the caller is
// mounted (null for no prefix), so no page has to remember to.
export function useTenantTitlePrefix(name: string | null) {
  useEffect(() => {
    tenantPrefix = name;
    render();
    return () => {
      tenantPrefix = null;
      render();
    };
  }, [name]);
}
