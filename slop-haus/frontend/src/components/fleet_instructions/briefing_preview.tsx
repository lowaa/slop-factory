import { useEffect, useId, useState } from "react";
import { Button, Dropdown } from "@slop-factory/ui";
import { apiClient, type BriefingFile, type FleetInstructions } from "@/api";
import "./briefing_preview.scss";

const NO_VERSION = "";

type State =
  | { status: "loading" }
  | { status: "error" }
  | { status: "loaded"; files: BriefingFile[] };

const TABS: Record<string, string> = {
  headless: "Headless run",
  pi: "pi",
};

// BriefingPreview shows the files agents are given, as the server renders
// them: the text is never held here, so it cannot drift from what a workspace
// gets. Collapsed until asked for, since nothing else on the page needs it.
export default function BriefingPreview({ data }: { data: FleetInstructions }) {
  const [open, setOpen] = useState(false);
  const [versionId, setVersionId] = useState(data.default_id ?? NO_VERSION);
  const [tab, setTab] = useState<string | null>(null);
  const [state, setState] = useState<State>({ status: "loading" });
  const panelId = useId();

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setState({ status: "loading" });
    apiClient
      .getFleetBriefings(versionId === NO_VERSION ? null : versionId)
      .then((files) => {
        if (!cancelled) setState({ status: "loaded", files });
      })
      .catch(() => {
        if (!cancelled) setState({ status: "error" });
      });
    return () => {
      cancelled = true;
    };
  }, [open, versionId]);

  const versionOptions = [
    { value: NO_VERSION, label: "None" },
    ...data.versions.map((v) => ({
      value: v.id,
      label: `${v.name} v${v.version}${v.id === data.default_id ? " (default)" : ""}`,
    })),
  ];

  const files = state.status === "loaded" ? state.files : [];
  const current = files.find((f) => f.name === tab) ?? files[0];

  return (
    <div className="briefing-preview">
      <Button size="sm" aria-expanded={open} aria-controls={panelId} onClick={() => setOpen((o) => !o)}>
        {open ? "Hide" : "Show"} the briefing agents receive
      </Button>
      {open && (
        <div id={panelId} className="briefing-preview__panel">
          <p className="briefing-preview__hint">
            This is what the current server would write to a workspace; a workspace that has not been updated may still
            hold an older copy. It is slop-haus&rsquo;s part only: agents also follow the repo&rsquo;s own CLAUDE.md and
            the MCP tool descriptions and notices. Each agent&rsquo;s name appears where it says &lt;agent-name&gt;.
          </p>
          <div className="briefing-preview__field">
            <Dropdown<string>
              label="Fleet instructions shown"
              value={versionId}
              options={versionOptions}
              onChange={setVersionId}
            />
          </div>
          {state.status === "error" && (
            <p role="alert" className="briefing-preview__error">
              Failed to load the briefing.
            </p>
          )}
          {state.status === "loading" && <p>Loading the briefing…</p>}
          {current && (
            <>
              <div role="tablist" aria-label="Briefing files" className="briefing-preview__tabs">
                {files.map((f) => (
                  <Button
                    key={f.name}
                    role="tab"
                    size="sm"
                    variant={f === current ? "primary" : undefined}
                    aria-selected={f === current}
                    onClick={() => setTab(f.name)}
                  >
                    {TABS[f.name] ?? f.name}
                  </Button>
                ))}
              </div>
              <p className="briefing-preview__hint">
                Written to <code>{current.path}</code>
              </p>
              <pre role="tabpanel" className="briefing-preview__text">
                {current.protocol}
                <FleetSection text={current.fleet} />
              </pre>
            </>
          )}
        </div>
      )}
    </div>
  );
}

// FleetSection marks the owners' own lines, the quoted ones, and leaves the
// paragraph the server wraps them in as it is: that framing is part of what
// the owners need to see.
function FleetSection({ text }: { text: string }) {
  if (text === "") return null;
  const lines = text.split("\n");
  return (
    <span className="briefing-preview__fleet" data-testid="fleet-section">
      {lines.map((line, i) => {
        const end = i < lines.length - 1 ? "\n" : "";
        return line.startsWith(">") ? (
          <mark key={i} className="briefing-preview__owner">
            {line}
            {end}
          </mark>
        ) : (
          <span key={i}>
            {line}
            {end}
          </span>
        );
      })}
    </span>
  );
}
