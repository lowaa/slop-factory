import { RESERVED_ATTRIBUTES, type Tier } from "./tier";
import { scrubUrlsInText, toRouteTemplate, type RouteTemplateResolver } from "./route_template";
import type { Attrs } from "./logger";

// Keys whose value is a URL. Each still has to be on the allowlist, and is
// then exported as ROUTE_ATTRIBUTE, holding the template.
const URL_ATTRIBUTES: ReadonlySet<string> = new Set(["url", "url.full", "url.path", "http.url", "page.url"]);
export const ROUTE_ATTRIBUTE = "http.route";

export interface AttributeAllowlist {
  // Allowed in every tier.
  common?: readonly string[];
  // Allowed only in that tier, so analytics can be narrower than
  // operational, or the reverse.
  operational?: readonly string[];
  analytics?: readonly string[];
  interaction_recording?: readonly string[];
}

export interface FilterResult {
  attributes: Record<string, string | number | boolean | Array<string | number | boolean>>;
  dropped: string[];
}

type Primitive = string | number | boolean;

function isPrimitive(value: unknown): value is Primitive {
  return typeof value === "string" || typeof value === "number" || typeof value === "boolean";
}

// Deliberately flat: an object or nested array can carry anything under a
// key that was allowlisted for a scalar.
function cleanValue(value: unknown, resolve?: RouteTemplateResolver): Primitive | Primitive[] | undefined {
  const scrub = (v: Primitive) => (typeof v === "string" ? scrubUrlsInText(v, resolve) : v);
  if (isPrimitive(value)) return scrub(value);
  if (Array.isArray(value) && value.every(isPrimitive)) return value.map(scrub);
  return undefined;
}

// Applies the allowlist: only listed keys leave, everything else is
// reported in `dropped` so the caller can count it.
export class AttributeFilter {
  private readonly allowed: Record<Tier, ReadonlySet<string>>;
  private readonly resolveRoute: RouteTemplateResolver | undefined;

  constructor(allowlist: AttributeAllowlist, resolveRoute?: RouteTemplateResolver) {
    this.resolveRoute = resolveRoute;
    const common = allowlist.common ?? [];
    this.allowed = {
      operational: new Set([...common, ...(allowlist.operational ?? [])]),
      analytics: new Set([...common, ...(allowlist.analytics ?? [])]),
      interaction_recording: new Set([...common, ...(allowlist.interaction_recording ?? [])]),
    };
  }

  filter(tier: Tier, attrs: Attrs | undefined): FilterResult {
    const result: FilterResult = { attributes: {}, dropped: [] };
    if (!attrs) return result;

    for (const [key, raw] of Object.entries(attrs)) {
      // An undefined value carries nothing, so there is nothing to drop.
      if (raw === undefined) continue;

      if (RESERVED_ATTRIBUTES.has(key) || !this.allowed[tier].has(key)) {
        result.dropped.push(key);
        continue;
      }
      if (URL_ATTRIBUTES.has(key)) {
        if (typeof raw === "string") result.attributes[ROUTE_ATTRIBUTE] = toRouteTemplate(raw, this.resolveRoute);
        else result.dropped.push(key);
        continue;
      }
      const value = cleanValue(raw, this.resolveRoute);
      if (value === undefined) result.dropped.push(key);
      else result.attributes[key] = value;
    }
    return result;
  }
}
