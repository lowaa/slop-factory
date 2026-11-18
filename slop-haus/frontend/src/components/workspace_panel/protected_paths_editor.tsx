import { useState } from "react";
import { Button } from "@slop-factory/ui";
import type { ManifestRepo } from "@/api";
import { firstMatchingRule } from "./protected_path_match";

type ProtectedPathRule = NonNullable<ManifestRepo["protected_paths"]>[number];

// What to weigh, offered as prompts and never as values: every tenant differs.
const THINGS_TO_CONSIDER = [
  "deployment and CI configuration",
  "database migrations",
  "authentication and secret handling",
  "real users' data",
  "agent instructions (CLAUDE.md, briefings)",
  "the steward's own rules and the merge gate",
];

export function ProtectedPathsExplainer() {
  return (
    <div className="manifest-section__hint" data-testid="protected-paths-help">
      <p>
        <strong>What a protected path does.</strong> A change or ticket touching one is always the owner&rsquo;s
        decision for the steward&rsquo;s judgment categories (approving reviews, picking discussion answers, opening
        tickets), at every autonomy level and override. It does not affect workers, scopes or merging.
      </p>
      <p>
        <strong>How matching works</strong>, the same as shared paths. Paths are relative to the repo&rsquo;s root. A
        trailing <code>/</code> covers everything under that directory (<code>infrastructure/</code>); otherwise it is
        a glob (<code>*.env</code>, <code>server/internal/erasure*</code>) where <code>*</code> and{" "}
        <code>?</code> do not cross a <code>/</code>.
      </p>
      <p>
        <strong>Why only owners edit it.</strong> An agent that could edit this list would choose its own limits.
      </p>
      <p>
        <strong>Things to consider protecting</strong> (nothing is filled in for you):
      </p>
      <ul>
        {THINGS_TO_CONSIDER.map((t) => (
          <li key={t}>{t}</li>
        ))}
      </ul>
    </div>
  );
}

export function NoProtectedPathsWarning() {
  return (
    <p className="manifest-section__warning">
      With no protected paths, and any steward judgment category set above <code>human</code>, nothing is protected:
      the steward may decide those on any change.
    </p>
  );
}

function PathPreview({ rules, repoNumber }: { rules: ProtectedPathRule[]; repoNumber: number }) {
  const [path, setPath] = useState("");
  const hit = firstMatchingRule(rules, path);
  return (
    <div className="manifest-section__field">
      <label htmlFor={`protected-preview-${repoNumber}`}>Preview a path of repo {repoNumber}</label>
      <input
        id={`protected-preview-${repoNumber}`}
        value={path}
        placeholder="infrastructure/k8s/prod.yaml"
        onChange={(e) => setPath(e.target.value)}
      />
      {path.trim() !== "" && (
        <p role="status" className="manifest-section__hint" aria-label={`Preview result of repo ${repoNumber}`}>
          {hit ? (
            <>
              Protected by <code>{hit.path.trim()}</code>.
            </>
          ) : (
            "Not protected by any rule above."
          )}
        </p>
      )}
    </div>
  );
}

export default function ProtectedPathsEditor({ repoIndex, rules, onChange }: {
  repoIndex: number;
  rules: ProtectedPathRule[];
  onChange: (rules: ProtectedPathRule[]) => void;
}) {
  const n = repoIndex + 1;
  const update = (k: number, patch: Partial<ProtectedPathRule>) =>
    onChange(rules.map((r, j) => (j === k ? { ...r, ...patch } : r)));
  return (
    <fieldset className="manifest-section__rules-edit">
      <legend>Protected paths of repo {n}</legend>
      <ProtectedPathsExplainer />
      {rules.filter((r) => r.path.trim() !== "").length === 0 && <NoProtectedPathsWarning />}
      {rules.map((rule, k) => (
        <div key={k} className="manifest-section__edit-row">
          <input
            aria-label={`Protected path ${k + 1} of repo ${n}`}
            value={rule.path}
            placeholder="infrastructure/"
            onChange={(e) => update(k, { path: e.target.value })}
          />
          <input
            aria-label={`Reason, protected path ${k + 1} of repo ${n}`}
            value={rule.reason ?? ""}
            placeholder="why (optional)"
            onChange={(e) => update(k, { reason: e.target.value })}
          />
          <Button variant="danger" onClick={() => onChange(rules.filter((_, j) => j !== k))}>
            Remove protected path {k + 1} of repo {n}
          </Button>
        </div>
      ))}
      <div className="manifest-section__actions">
        <Button onClick={() => onChange([...rules, { path: "" }])}>Add protected path to repo {n}</Button>
      </div>
      <PathPreview rules={rules} repoNumber={n} />
    </fieldset>
  );
}
