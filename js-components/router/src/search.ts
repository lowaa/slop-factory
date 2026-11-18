// A search string is parsed into flat string values, and a search object is
// written back the same way. TanStack's default JSON-parses each value, so
// `?agent=42` would arrive as the number 42 and a string "42" would be
// written back as `?agent=%2242%22` -- and the router rewrites the address
// bar to its own serialization on first load, so that would change URLs
// other code reads straight from window.location. Flat strings keep the URL
// exactly as it came in; a route's validateSearch does any typing.
export type RawSearch = Record<string, string>;

export function parseSearch(searchStr: string): RawSearch {
  // First value wins for a repeated key, as URLSearchParams.get does. No
  // prototype, so a key like "constructor" or "__proto__" is just a key.
  const result: RawSearch = Object.create(null);
  for (const [key, value] of new URLSearchParams(searchStr)) {
    if (!Object.hasOwn(result, key)) result[key] = value;
  }
  return result;
}

export function stringifySearch(search: Record<string, unknown>): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(search)) {
    if (value === undefined || value === null) continue;
    params.set(key, String(value));
  }
  const str = params.toString();
  return str ? `?${str}` : "";
}

// Building blocks for plain validateSearch functions, so routes can declare
// their search params without a schema library.

// optionalString returns a non-empty string value, or undefined for a
// missing, empty or non-string one.
export function optionalString(raw: Record<string, unknown>, key: string): string | undefined {
  const value = raw[key];
  return typeof value === "string" && value !== "" ? value : undefined;
}

// noSearch is the validator for a route that takes no search params yet.
// Declaring it still matters: it is where the route's params go once it
// has some, and a route without one would accept any search at all.
export function noSearch(): Record<string, never> {
  return {};
}

// optionalOneOf returns the value at key when it is one of allowed, or
// undefined otherwise -- for a param naming one of a fixed set (a tab, a
// mode), where an unknown value from a hand-edited or stale link should
// read as "not given" rather than fail the route.
export function optionalOneOf<T extends string>(
  raw: Record<string, unknown>,
  key: string,
  allowed: readonly T[],
): T | undefined {
  const value = raw[key];
  return typeof value === "string" && (allowed as readonly string[]).includes(value) ? (value as T) : undefined;
}
