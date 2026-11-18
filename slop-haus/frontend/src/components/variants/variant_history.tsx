import type { FleetInstructionsVersion, HarnessSettingDefinition, VariantVersion } from "@/api";
import { Button } from "@slop-factory/ui";
import { HARNESS_LABELS } from "@/domain/harnesses";
import Timestamp from "@/shared/timestamp/timestamp";
import { pinnedLabel, settingsLine } from "./variant_labels";
import "./variants.scss";

export interface VariantActions {
  busy: boolean;
  onStartFrom: (version: VariantVersion) => void;
  onArchive: (id: string) => void;
  onUnarchive: (id: string) => void;
  onArchiveGroup: (name: string) => void;
  onUnarchiveGroup: (name: string) => void;
}

// groupByName keeps the server's order: by name, newest version first.
function groupByName(versions: VariantVersion[]): [string, VariantVersion[]][] {
  const groups = new Map<string, VariantVersion[]>();
  for (const v of versions) {
    if (!groups.has(v.name)) groups.set(v.name, []);
    groups.get(v.name)!.push(v);
  }
  return Array.from(groups);
}

interface VariantHistoryProps {
  versions: VariantVersion[];
  definitions: HarnessSettingDefinition[];
  fleetVersions: FleetInstructionsVersion[];
  actions: VariantActions;
}

// VariantHistory lists every saved version, grouped by name. A version is
// shown and never edited: the only way to change one is to save the next.
export default function VariantHistory({ versions, definitions, fleetVersions, actions }: VariantHistoryProps) {
  if (versions.length === 0) return <p className="variants__hint">No variants yet.</p>;
  return (
    <div className="variants__history">
      {groupByName(versions).map(([name, group]) => {
        const anyLive = group.some((v) => !v.archived);
        const anyArchived = group.some((v) => v.archived);
        return (
          <div key={name}>
            <div className="variants__group-header">
              <h4 className="variants__heading">{name}</h4>
              {!anyLive && <span className="variants__badge">archived</span>}
              {anyLive && (
                <Button
                  size="sm"
                  disabled={actions.busy}
                  aria-label={`Archive every ${name} version`}
                  onClick={() => actions.onArchiveGroup(name)}
                >
                  Archive all
                </Button>
              )}
              {anyArchived && (
                <Button
                  size="sm"
                  disabled={actions.busy}
                  aria-label={`Unarchive every ${name} version`}
                  onClick={() => actions.onUnarchiveGroup(name)}
                >
                  Unarchive all
                </Button>
              )}
            </div>
            <ul className="variants__group">
              {group.map((v) => (
                <li key={v.id} className="variants__version">
                  <div>
                    <strong>
                      {v.name} v{v.version}
                    </strong>
                    {v.archived && <span className="variants__badge">archived</span>}{" "}
                    <span className="variants__hint">
                      saved <Timestamp at={v.created_at} />
                    </span>
                  </div>
                  <dl className="variants__facts">
                    <dt>Harness</dt>
                    <dd>{HARNESS_LABELS[v.harness] ?? v.harness}</dd>
                    <dt>Settings</dt>
                    <dd>{settingsLine(definitions, v)}</dd>
                    <dt>Fleet instructions</dt>
                    <dd>{pinnedLabel(fleetVersions, v.fleet_instructions_version_id)}</dd>
                  </dl>
                  <div className="variants__actions">
                    <Button
                      size="sm"
                      variant="ghost"
                      aria-label={`Start a new version from ${v.name} v${v.version}`}
                      onClick={() => actions.onStartFrom(v)}
                    >
                      New version from this
                    </Button>
                    <Button
                      size="sm"
                      disabled={actions.busy}
                      aria-label={`${v.archived ? "Unarchive" : "Archive"} ${v.name} v${v.version}`}
                      onClick={() => (v.archived ? actions.onUnarchive(v.id) : actions.onArchive(v.id))}
                    >
                      {v.archived ? "Unarchive" : "Archive"}
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
          </div>
        );
      })}
    </div>
  );
}
