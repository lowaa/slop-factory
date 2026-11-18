import type { ConsentApi, ConsentState, PurposeState } from "../types";

export function storyPurpose(id: string, over: Partial<PurposeState> = {}): PurposeState {
  return {
    id,
    kind: "cookie_category",
    necessary: id === "necessary",
    granted: id === "necessary",
    needsDecision: id !== "necessary",
    gpcLocked: false,
    documentKind: "cookie_policy",
    documentVersion: "2026-10-01",
    basedOn: 0,
    ...over,
  };
}

export const FRESH_VISIT: ConsentState = {
  purposes: [storyPurpose("necessary"), storyPurpose("analytics"), storyPurpose("marketing")],
  signedIn: false,
  gpcHonoured: false,
};

// A stand-in for the server, for stories only: it takes every decision as
// given. The tests that matter for the server's rules run against the real
// one (go-privacy/consent).
export function echoApi(initial: ConsentState): ConsentApi {
  let state = initial;
  return {
    get: async () => state,
    put: async (_source, decisions) => {
      state = {
        ...state,
        purposes: state.purposes.map((p) => {
          const d = decisions.find((x) => x.purpose === p.id);
          return d ? { ...p, granted: d.granted && !p.gpcLocked, needsDecision: false, basedOn: p.basedOn + 1 } : p;
        }),
      };
      return state;
    },
  };
}
