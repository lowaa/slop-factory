const HOW_RESOLVED: Record<string, string> = {
  migration: "renumber to follow what landed",
  regenerate: "re-run the generator",
  merge: "merge the base branch and resolve by hand",
};

// SharedOverlapTag marks an overlap that lies inside a shared path of the
// workspace: it blocks nothing, and the kind says how a clash there is
// settled when main is merged.
// With lockingOff, a real conflict is marked too: the tenant does not block on it.
export default function SharedOverlapTag({ resolve, lockingOff }: { resolve?: string; lockingOff?: boolean }) {
  if (!resolve && lockingOff) {
    return (
      <>
        {" "}
        <em>(scope locking is off, blocks nothing)</em>
      </>
    );
  }
  if (!resolve) return null;
  return (
    <>
      {" "}
      <em>
        (shared path, blocks nothing; on a clash: {resolve}, {HOW_RESOLVED[resolve] ?? "see the workspace page"})
      </em>
    </>
  );
}
