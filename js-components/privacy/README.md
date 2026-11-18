# @slop-factory/privacy

Browser side of the privacy framework in `go-privacy/README.md` (read that first: the model, the routes, GPC in §7). Two parts, one package:

- `@slop-factory/privacy/legal_docs`: the legal document pages and footer links.
- `@slop-factory/privacy/consent`: the consent banner, preferences dialog, script gate and GPC handling, described here.

## Consent

The browser never decides. `ConsentProvider` shows what `GET /api/privacy/consent` reports and sends decisions to `PUT` on the same path. It always asks the server, signed in or not: the anonymous cookie is httpOnly and written only by the server. Until the server has answered nothing is granted, and a purpose it doesn't report (a capability flag that is off) is neither shown nor grantable.

```tsx
const store = new ConsentStore({
  api: createFetchConsentApi({ fetch: sessionFetch, refreshSession: () => session.refresh() }),
  name: "app-consent", // names the cross-tab lock and channel
});

// Outside React, e.g. where the logger is built: the analytics tier.
new OtelLogger({ hasAnalyticsConsent: store.check("analytics"), /* ... */ });

<ConsentProvider store={store} documentHref={legalHref} copy={purposeCopy}>
  <App />
  <LegalFooterLinks href={legalHref}><CookiePreferencesButton /></LegalFooterLinks>
  {/* settings page */ <ConsentSettings />}
</ConsentProvider>
```

`ConsentProvider` renders the banner and the preferences dialog itself (`ui={false}` to place them yourself). `useConsent("analytics")` gives `{ available, granted, needsDecision, decide }`; `useConsentCheck(purpose)` is the logger's check as a stable function that reads the live state, so a withdrawal applies to the next record.

- **Banner**: shown while a non-necessary cookie purpose needs a decision. Reject all and Accept all share one set of props (`EQUAL_ACTION_PROPS`) and a story compares their computed styles. Manage opens the dialog. It doesn't block the app and takes no focus until used. Processing purposes (health data, LLM) are not in the banner or the dialog: they have their own prompts.
- **Preferences dialog**: one switch per cookie purpose, none pre-ticked, necessary ones shown on and fixed. Opened from the banner, `CookiePreferencesButton` (the "Cookie preferences" footer entry) and `ConsentSettings` (the settings page), so withdrawing is as easy as giving. Purpose names and descriptions are legal copy: pass `copy`, keyed by purpose id; the defaults are placeholders.
- **Do Not Sell or Share**: `DoNotSellButton` (the CCPA entry, next to `CookiePreferencesButton` in the footer) denies every `SaleOrShare` purpose in one step, recorded like any other decision (`settings`), so it works without GPC. Pass `purposes` when an app has more than `marketing`. There is deliberately no "Limit the Use of My Sensitive Personal Information" entry: an app that uses sensitive data only for the service the person asked for needn't offer it (decided in the consent wiring; the notice at collection says so).
- **Script gate**: `<ConsentScript purpose id load />` or `loadWhenConsented(store, purpose, loader, { id })` is the only sanctioned way to load an optional script. Nothing loads until the purpose is granted; withdrawal aborts a load in flight (`signal`), calls the teardown the loader returned, and a script that finished loading after withdrawal is torn down at once. A loader may run again if consent is granted again, so make it idempotent. **Adding a vendor also needs the app's CSP widened** (`script-src`/`connect-src` in its `security_headers.conf`) and the data inventory updated; a vendor that uses the data for its own purposes makes its purpose `SaleOrShare` (go-privacy README §12).
- **GPC**: the server applies it and reports each purpose it is denying (`gpcLocked`). The client shows that signal as honoured, keeps those purposes off and unchangeable, and on Accept all denies them rather than send a grant the server would refuse. `navigator.globalPrivacyControl` is read only to report `gpc.signalled`; the browser can't tell which purposes are sale/share, so it never decides one on its own.
- **Writes**: one at a time within a tab, and across tabs under a Web Lock (the anonymous cookie is last-write-wins), then other tabs reload through a BroadcastChannel. Decisions carry `based_on` and the document version shown. After a stale write (`consent_changed`, `document_changed`, `gpc_active`) the state is reloaded.
- **Rejected sessions**: a consent write on a refused session credential is answered `403 credential_rejected` and never recorded. `createFetchConsentApi` refreshes the session through `refreshSession` and sends the write again exactly once; nothing is shown in between, and a second refusal is reported as an error with nothing recorded. Use `refreshSession` with a plain fetch that adds the CSRF token, or pass the session client's own retrying fetch and omit it. The package doesn't depend on `@slop-factory/auth`.
