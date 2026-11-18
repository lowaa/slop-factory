// At most 15 digits, so every id this accepts is a safe integer and turns
// back into the same digits.
const POSITIVE_INT = /^[1-9]\d{0,14}$/;

// The canonical 8-4-4-4-12 hex form, either case. Version and variant bits
// are not checked: a route only needs to know the segment is shaped like an
// id, and the server decides whether one exists.
const UUID = /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;

// checkedParam is the `params` option for a route whose path param key must
// match pattern: its components get the param as toValue makes it, and a
// path whose param does not match does not match the route at all, so it
// falls through to whatever handles an unknown path rather than rendering
// with a nonsense id.
//
// TOther names the route's other path params, which stay strings: the
// router checks a route's parsed params cover every param in its path.
function checkedParam<TKey extends string, TOther extends string, TValue>(
  key: TKey,
  pattern: RegExp,
  toValue: (raw: string) => TValue,
  toRaw: (value: TValue) => string,
) {
  type Raw = Record<TKey | TOther, string>;
  type Parsed = Record<TOther, string> & Record<TKey, TValue>;
  return {
    parse: (raw: Raw): Parsed | false =>
      pattern.test(raw[key]) ? { ...raw, [key]: toValue(raw[key]) } : false,
    stringify: (params: Parsed): Raw => ({ ...params, [key]: toRaw(params[key]) }),
  };
}

// positiveIntParam is the `params` option for a route whose path param key
// is a positive integer id (/tickets/$ticketId, say): the route's
// components get a number, and "abc", "0", "007" or "1e3" do not match the
// route. For other params in the same path, name them in TOther (e.g.
// positiveIntParam<"itemId", "orgId">("itemId") for
// /orgs/$orgId/items/$itemId).
export function positiveIntParam<TKey extends string, TOther extends string = never>(key: TKey) {
  return checkedParam<TKey, TOther, number>(key, POSITIVE_INT, Number, String);
}

// uuidParam is the `params` option for a route whose path param key is a
// UUID (/invoices/$id, say). The id stays the string it was in the path,
// unchanged in case, so a link built from it is the one that was followed.
export function uuidParam<TKey extends string, TOther extends string = never>(key: TKey) {
  return checkedParam<TKey, TOther, string>(key, UUID, String, String);
}
