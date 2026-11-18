// Reject all and Accept all are one shared set of props, not two that happen
// to match, so they can't drift apart: consent has to be as easy to refuse
// as to give, and neither may be made to look like the default.
export const EQUAL_ACTION_PROPS = { variant: "secondary", size: "md" } as const;
