// Turns a URL into a route template: no origin, query string, fragment or
// ids, which can carry names, tokens or record identifiers.

// Matches the router's own template for a pathname, e.g. "/results/$id".
// Returns undefined when the pathname matches no route.
export type RouteTemplateResolver = (pathname: string) => string | undefined;

const ID_SEGMENT = /^(\d+|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[0-9a-f]{16,}|[A-Za-z0-9_-]{24,})$/i;
// Not anchored at the end of a word: a trailing ')' or '.' in prose after
// a URL is cut by the character class, not left for the template.
const URL_IN_TEXT = /\bhttps?:\/\/[^\s"'<>)\]]+/gi;

// Fallback for a pathname the router has no template for: it can't tell
// which segments are ids, so it removes the ones that look like one.
export function genericRouteTemplate(pathname: string): string {
  return pathname
    .split("/")
    .map((segment) => (ID_SEGMENT.test(segment) ? ":id" : segment))
    .join("/");
}

function pathnameOf(url: string): string {
  // The base only makes a relative path parseable; its origin is discarded.
  try {
    return new URL(url, "http://route-template.invalid").pathname;
  } catch {
    return "/";
  }
}

export function toRouteTemplate(url: string, resolve?: RouteTemplateResolver): string {
  const pathname = pathnameOf(url);
  return resolve?.(pathname) ?? genericRouteTemplate(pathname);
}

// Replaces every absolute URL inside free text (an error message or stack)
// with its template.
export function scrubUrlsInText(text: string, resolve?: RouteTemplateResolver): string {
  return text.replace(URL_IN_TEXT, (url) => toRouteTemplate(url, resolve));
}
