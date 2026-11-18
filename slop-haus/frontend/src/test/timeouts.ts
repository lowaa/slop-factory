// A findBy* for the first paint of the app or of a lazily loaded page waits
// this long: on a loaded CI runner the module transform and import alone can
// pass setup.ts's asyncUtilTimeout. Steps after it use the default.
export const FIRST_PAINT_TIMEOUT_MS = 10_000;
