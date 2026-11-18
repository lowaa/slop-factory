import { ConsentStore, type ConsentApi, type ConsentState } from "@slop-factory/privacy/consent";

function purpose(id: string, needsDecision: boolean, granted = false): ConsentState["purposes"][number] {
  return {
    id,
    kind: "cookie_category",
    necessary: id === "necessary",
    granted: id === "necessary" || granted,
    needsDecision: id !== "necessary" && needsDecision,
    gpcLocked: false,
    documentKind: "cookie_policy",
    documentVersion: "2026-10-03.1",
    basedOn: 0,
  };
}

// What the server reports to a visitor who has not chosen yet, and to one who
// has refused everything.
export const FRESH_VISIT: ConsentState = {
  purposes: [purpose("necessary", false), purpose("analytics", true), purpose("marketing", true)],
  signedIn: false,
  gpcHonoured: false,
};
export const ALL_REFUSED: ConsentState = {
  purposes: [purpose("necessary", false), purpose("analytics", false), purpose("marketing", false)],
  signedIn: false,
  gpcHonoured: false,
};

// A consent API with no network behind it: it answers with `initial`, and
// takes each decision as given. The server's own rules are tested against
// the real server (cmd/api's consent tests), not here.
export function stubConsentApi(initial: ConsentState): ConsentApi {
  let state = initial;
  return {
    get: async () => state,
    put: async (_source, decisions) => {
      state = {
        ...state,
        purposes: state.purposes.map((p) => {
          const d = decisions.find((x) => x.purpose === p.id);
          return d ? { ...p, granted: d.granted, needsDecision: false, basedOn: p.basedOn + 1 } : p;
        }),
      };
      return state;
    },
  };
}

// Refused by default, so a test that is not about consent never sees the
// banner.
export function makeConsentStore(initial: ConsentState = ALL_REFUSED): ConsentStore {
  return new ConsentStore({ api: stubConsentApi(initial), name: "slop-haus-test-consent", locks: null, createChannel: null });
}
