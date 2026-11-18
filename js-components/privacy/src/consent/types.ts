export type PurposeKind = "cookie_category" | "processing";

// What the decision was made through. The server records it with the
// decision as proof of where consent was given.
export type DecisionSource = "banner" | "settings" | "sign_up" | "prompt";

// One purpose as the server's consent route reports it. The server decides
// everything here (what is offered, what is in force, what needs asking);
// the browser only displays it.
export interface PurposeState {
  id: string;
  kind: PurposeKind;
  necessary: boolean;
  // What is effectively in force, not what the person last chose: a grant
  // under an out-of-date document, or one Global Privacy Control is
  // denying, reads false.
  granted: boolean;
  needsDecision: boolean;
  // Global Privacy Control is denying this purpose, so it can't be granted.
  gpcLocked: boolean;
  documentKind: string | null;
  // The version a decision on this purpose must cite.
  documentVersion: string | null;
  // Sent back with a decision so the server can refuse one made on a state
  // that has since changed.
  basedOn: number;
}

export interface ConsentState {
  purposes: readonly PurposeState[];
  signedIn: boolean;
  // The server saw the Global Privacy Control signal and applied it.
  gpcHonoured: boolean;
}

export interface Decision {
  purpose: string;
  granted: boolean;
}

export interface ConsentApi {
  get(): Promise<ConsentState>;
  put(source: DecisionSource, decisions: readonly Decision[], current: ConsentState): Promise<ConsentState>;
}

// The server's machine-readable refusal codes for a consent write, plus
// the two this client raises itself.
export type ConsentErrorCode =
  | "credential_rejected"
  | "consent_changed"
  | "document_changed"
  | "gpc_active"
  | "sign_in_required"
  | "invalid_purpose"
  | "necessary_purpose"
  | "duplicate_purpose"
  | "network"
  | "unknown";

export class ConsentApiError extends Error {
  readonly code: ConsentErrorCode;
  readonly status: number;

  constructor(code: ConsentErrorCode, status: number) {
    super(`consent request failed (${code})`);
    this.name = "ConsentApiError";
    this.code = code;
    this.status = status;
  }
}
