import { useId, useState } from "react";
import { Button, Tooltip } from "@slop-factory/ui";
import type { FleetInstructions, FleetInstructionsVersion } from "@/api";
import Timestamp from "@/shared/timestamp/timestamp";

// What a host lets the viewer do to a version. Leave it out and the history
// is read only.
export interface VersionActions {
  busy: boolean;
  onMakeDefault: (id: string) => void;
  onStartFrom: (version: FleetInstructionsVersion) => void;
  onArchive: (id: string) => void;
  onUnarchive: (id: string) => void;
  onArchiveGroup: (name: string) => void;
  onUnarchiveGroup: (name: string) => void;
}

const DEFAULT_BLOCKS_ARCHIVE = "The default can't be archived. Choose another default, or none, first.";
const GROUP_DEFAULT_BLOCKS_ARCHIVE =
  "This name holds the default version, so it can't be archived. Choose another default, or none, first.";

// ArchiveButton explains a refusal in a tooltip rather than only greying out.
// A disabled button takes no focus, so a keyboard user could never reach the
// reason; aria-disabled keeps it focusable and the click is ignored here.
function ArchiveButton({
  label,
  ariaLabel,
  blockedReason,
  busy,
  onClick,
}: {
  label: string;
  ariaLabel: string;
  blockedReason?: string;
  busy: boolean;
  onClick: () => void;
}) {
  if (!blockedReason) {
    return (
      <Button size="sm" variant="ghost" disabled={busy} aria-label={ariaLabel} onClick={onClick}>
        {label}
      </Button>
    );
  }
  return (
    <Tooltip text={blockedReason}>
      <Button size="sm" variant="ghost" aria-disabled="true" aria-label={ariaLabel}>
        {label}
      </Button>
    </Tooltip>
  );
}

function groupByName(versions: FleetInstructionsVersion[]): [string, FleetInstructionsVersion[]][] {
  const groups = new Map<string, FleetInstructionsVersion[]>();
  for (const v of versions) groups.set(v.name, [...(groups.get(v.name) ?? []), v]);
  return [...groups.entries()];
}

// VersionHistory lists every saved version, grouped by name. The whole list
// folds away, and so does each version; only the default starts open, since
// that is the one the fleet is told.
export default function VersionHistory({ data, actions }: { data: FleetInstructions; actions?: VersionActions }) {
  const [open, setOpen] = useState(true);
  const panelId = useId();
  if (data.versions.length === 0) return null;
  return (
    <div className="fleet-instructions__history">
      <Button size="sm" aria-expanded={open} aria-controls={panelId} onClick={() => setOpen((o) => !o)}>
        {open ? "Hide" : "Show"} version history ({data.versions.length})
      </Button>
      {open && (
        <div id={panelId} className="fleet-instructions__history-panel">
          {groupByName(data.versions).map(([groupName, versions]) => (
            <div key={groupName}>
              <GroupHeader name={groupName} versions={versions} defaultId={data.default_id} actions={actions} />
              <ul className="fleet-instructions__group">
                {versions.map((v) => (
                  <VersionEntry key={v.id} version={v} isDefault={v.id === data.default_id} actions={actions} />
                ))}
              </ul>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function GroupHeader({
  name,
  versions,
  defaultId,
  actions,
}: {
  name: string;
  versions: FleetInstructionsVersion[];
  defaultId: string | null;
  actions?: VersionActions;
}) {
  const anyLive = versions.some((v) => !v.archived);
  const anyArchived = versions.some((v) => v.archived);
  const holdsDefault = versions.some((v) => v.id === defaultId);
  return (
    <div className="fleet-instructions__group-header">
      <h4 className="fleet-instructions__heading">{name}</h4>
      {!anyLive && <span className="fleet-instructions__badge">archived</span>}
      {actions && anyLive && (
        <ArchiveButton
          label="Archive all"
          ariaLabel={`Archive every ${name} version`}
          blockedReason={holdsDefault ? GROUP_DEFAULT_BLOCKS_ARCHIVE : undefined}
          busy={actions.busy}
          onClick={() => actions.onArchiveGroup(name)}
        />
      )}
      {actions && anyArchived && (
        <ArchiveButton
          label="Unarchive all"
          ariaLabel={`Unarchive every ${name} version`}
          busy={actions.busy}
          onClick={() => actions.onUnarchiveGroup(name)}
        />
      )}
    </div>
  );
}

function VersionEntry({
  version: v,
  isDefault,
  actions,
}: {
  version: FleetInstructionsVersion;
  isDefault: boolean;
  actions?: VersionActions;
}) {
  const [open, setOpen] = useState(isDefault);
  const bodyId = useId();
  return (
    <li className="fleet-instructions__version">
      <div>
        <Button
          size="sm"
          variant="ghost"
          aria-expanded={open}
          aria-controls={bodyId}
          aria-label={`${open ? "Collapse" : "Expand"} ${v.name} v${v.version}`}
          onClick={() => setOpen((o) => !o)}
        >
          <strong>
            {v.name} v{v.version}
          </strong>
        </Button>
        {isDefault && <span className="fleet-instructions__badge">default</span>}
        {v.archived && <span className="fleet-instructions__badge">archived</span>}{" "}
        <span className="fleet-instructions__hint">
          saved <Timestamp at={v.created_at} />
        </span>
        {actions && (
          <ArchiveButton
            label={v.archived ? "Unarchive" : "Archive"}
            ariaLabel={`${v.archived ? "Unarchive" : "Archive"} ${v.name} v${v.version}`}
            blockedReason={isDefault && !v.archived ? DEFAULT_BLOCKS_ARCHIVE : undefined}
            busy={actions.busy}
            onClick={() => (v.archived ? actions.onUnarchive(v.id) : actions.onArchive(v.id))}
          />
        )}
      </div>
      {open && (
        <div id={bodyId} className="fleet-instructions__entry">
          <pre className="fleet-instructions__text">{v.body}</pre>
          {actions && (
            <div className="fleet-instructions__actions">
              {!isDefault && !v.archived && (
                <Button
                  size="sm"
                  disabled={actions.busy}
                  aria-label={`Make ${v.name} v${v.version} the default`}
                  onClick={() => actions.onMakeDefault(v.id)}
                >
                  Make default
                </Button>
              )}
              <Button
                size="sm"
                variant="ghost"
                aria-label={`Start a new version from ${v.name} v${v.version}`}
                onClick={() => actions.onStartFrom(v)}
              >
                New version from this
              </Button>
            </div>
          )}
        </div>
      )}
    </li>
  );
}
