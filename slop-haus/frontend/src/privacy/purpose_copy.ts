import type { PurposeCopy } from "@slop-factory/privacy/consent";

// What the banner and preferences say for each purpose. Legal copy: a
// placeholder until the cookie policy is reviewed (go-privacy/README.md,
// section 10). It says what is true today: slop-haus sets necessary storage
// only, and these choices are recorded so they apply the day it doesn't.
export const PURPOSE_COPY: Readonly<Record<string, PurposeCopy>> = {
  necessary: {
    name: "Strictly necessary",
    description: "Keeps you signed in and the service secure, and remembers these choices. These can't be turned off.",
  },
  analytics: {
    name: "Analytics",
    description: "slop-haus sets no analytics storage today. If that changes, it will only happen if you agree here.",
  },
  marketing: {
    name: "Marketing",
    description:
      "slop-haus sets no marketing storage today and shows no advertising. If that changes, it will only happen if you agree here.",
  },
};
