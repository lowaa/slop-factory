// The tenant setting holding slop-review's provider choice, as JSON. The shapes
// and rules below mirror slop-review/report/providerconfig/providerconfig.go,
// the one the server and the reviewer validate with; the client has no
// endpoint for the provider list, so a provider added there is added here.
// The server stays the authority: this only spares a round trip.
export const REVIEW_PROVIDER_CONFIG = "slop_review.provider_config";

export interface ProviderInfo {
  // Efforts the provider accepts; empty when it takes none (it selects effort
  // by model slug).
  efforts: readonly string[];
}

// providerconfig.ReasoningEfforts
const REASONING_EFFORTS = ["none", "minimal", "low", "medium", "high", "xhigh", "max"] as const;

// providerconfig.Providers (names, sorted as ProviderNames() gives them) with
// AcceptsEffort folded into the efforts each takes.
export const REVIEW_PROVIDERS: Record<string, ProviderInfo> = {
  chatgpt: { efforts: REASONING_EFFORTS },
  claude: { efforts: ["low", "medium", "high", "xhigh", "max"] },
  codex: { efforts: REASONING_EFFORTS },
  openai: { efforts: [] },
  openrouter: { efforts: [] },
};

export const REVIEW_PROVIDER_NAMES = Object.keys(REVIEW_PROVIDERS).sort();

// providerconfig.TriggerClasses: the provider's own failures. config and
// other are local mistakes another provider would not fix.
export const TRIGGER_CLASSES = [
  { id: "usage_limit", label: "Usage limit" },
  { id: "credits", label: "Out of credits" },
  { id: "auth", label: "Auth (lost login)" },
  { id: "backend", label: "Backend error" },
] as const;

// providerconfig.DefaultTrigger
export const DEFAULT_TRIGGER: readonly string[] = ["usage_limit"];

// providerconfig.MaxModelBytes and modelPattern
const MAX_MODEL_BYTES = 128;
const MODEL_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._:/-]*$/;

export interface ChoiceDraft {
  provider: string;
  model: string;
  effort: string;
}

export interface ProviderDraft {
  primary: ChoiceDraft;
  fallback: ChoiceDraft | null;
  fallbackOn: string[];
}

export const EMPTY_CHOICE: ChoiceDraft = { provider: "", model: "", effort: "" };

export const EMPTY_DRAFT: ProviderDraft = { primary: EMPTY_CHOICE, fallback: null, fallbackOn: [] };

export function acceptedEfforts(provider: string): readonly string[] {
  return REVIEW_PROVIDERS[provider]?.efforts ?? [];
}

// providerconfig.DefaultReasoningEffort: what an empty effort means for a
// provider that accepts it.
const DEFAULT_REASONING_EFFORT = "xhigh";

// What an empty effort stands for on provider (providerconfig's EffortOrDefault).
export function defaultEffortLabel(provider: string): string {
  return acceptedEfforts(provider).includes(DEFAULT_REASONING_EFFORT) ? `Default (${DEFAULT_REASONING_EFFORT})` : "Model default";
}

function validateChoice(name: string, c: ChoiceDraft): string[] {
  const problems: string[] = [];
  if (!(c.provider in REVIEW_PROVIDERS)) {
    problems.push(`Choose the ${name} provider.`);
  }
  const model = c.model.trim();
  if (model !== "") {
    if (model.length > MAX_MODEL_BYTES) problems.push(`The ${name} model is longer than ${MAX_MODEL_BYTES} bytes.`);
    else if (!MODEL_PATTERN.test(model) || model.includes("//") || model.includes("..")) {
      problems.push(`The ${name} model must be a model slug (letters, digits and . _ : / -).`);
    }
  }
  if (c.effort !== "" && c.provider in REVIEW_PROVIDERS && !acceptedEfforts(c.provider).includes(c.effort)) {
    problems.push(`${c.provider} does not accept the ${name} reasoning effort "${c.effort}".`);
  }
  return problems;
}

// What Settings.Validate would refuse, as sentences for the form.
export function validateDraft(d: ProviderDraft): string[] {
  const problems = validateChoice("primary", d.primary);
  if (d.fallback) {
    problems.push(...validateChoice("fallback", d.fallback));
    if (d.fallback.provider !== "" && d.fallback.provider === d.primary.provider) {
      problems.push("The fallback provider must differ from the primary's: it would fail the same way.");
    }
    if (d.fallbackOn.length === 0) problems.push("Choose at least one failure the fallback is taken on.");
  }
  return problems;
}

function choiceJson(c: ChoiceDraft) {
  return { provider: c.provider, model: c.model.trim(), reasoning_effort: c.effort };
}

// The stored form: the keys of providerconfig.Settings. fallback_on is
// omitted without a fallback, which the server refuses to see set.
export function serializeDraft(d: ProviderDraft): string {
  return JSON.stringify({
    primary: choiceJson(d.primary),
    ...(d.fallback ? { fallback: choiceJson(d.fallback), fallback_on: [...d.fallbackOn] } : {}),
  });
}

function choiceFrom(raw: unknown): ChoiceDraft {
  const c = (raw ?? {}) as Record<string, unknown>;
  const text = (v: unknown) => (typeof v === "string" ? v : "");
  return { provider: text(c.provider), model: text(c.model), effort: text(c.reasoning_effort) };
}

// The draft a stored value stands for. A blank value is "none set"; one that
// is not readable as settings is also an empty draft, flagged unreadable so
// the form can say saving replaces it.
export function parseStored(stored: string | null): { draft: ProviderDraft; set: boolean; unreadable: boolean } {
  if (stored === null || stored.trim() === "") return { draft: EMPTY_DRAFT, set: false, unreadable: false };
  try {
    const raw = JSON.parse(stored) as Record<string, unknown> | null;
    if (raw === null || typeof raw !== "object" || typeof raw.primary !== "object") throw new Error("not settings");
    const on = Array.isArray(raw.fallback_on) ? raw.fallback_on.filter((c): c is string => typeof c === "string") : [];
    return {
      draft: { primary: choiceFrom(raw.primary), fallback: raw.fallback ? choiceFrom(raw.fallback) : null, fallbackOn: on },
      set: true,
      unreadable: false,
    };
  } catch {
    return { draft: EMPTY_DRAFT, set: true, unreadable: true };
  }
}

export function describeChoice(c: ChoiceDraft): string {
  return `${c.provider}, ${c.model || "default model"}${c.effort ? `, ${c.effort} effort` : ""}`;
}
