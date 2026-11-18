export interface PurposeCopy {
  name: string;
  description: string;
}

// Placeholder wording for the purposes go-privacy documents. The real text
// is legal copy that comes from the app's legal documents, keyed by purpose
// id (go-privacy/README.md §4), so an app passes its own `copy` to the
// provider; these only keep a purpose legible until it does.
export const DEFAULT_PURPOSE_COPY: Readonly<Record<string, PurposeCopy>> = {
  necessary: {
    name: "Strictly necessary",
    description: "Keeps you signed in and the site secure, and remembers these choices. These can't be turned off.",
  },
  analytics: {
    name: "Analytics",
    description: "Helps us see which features are used and how long tasks take, so we can improve them.",
  },
  interaction_recording: {
    name: "Interaction recording",
    description: "Records how you move around the page, such as cursor movement and idle time, to find usability problems.",
  },
  marketing: {
    name: "Marketing",
    description: "Lets us and our partners show you relevant offers elsewhere.",
  },
};

export function purposeCopy(
  id: string,
  overrides: Readonly<Record<string, PurposeCopy>> | undefined,
): PurposeCopy {
  return overrides?.[id] ?? DEFAULT_PURPOSE_COPY[id] ?? { name: id, description: "" };
}
