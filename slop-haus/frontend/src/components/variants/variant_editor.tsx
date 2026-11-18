import { useId, useMemo, useState } from "react";
import type { FleetInstructionsVersion, HarnessSettingDefinition, VariantVersion } from "@/api";
import { Dropdown, type DropdownOption } from "@slop-factory/ui";
import HarnessConfigForm from "@/shared/harness_config_form/harness_config_form";
import { HARNESSES } from "@/domain/harnesses";

import type { VariantDraft } from "./variant_draft";
import "./variants.scss";

interface VariantEditorProps {
  // Seeds the form; remounted (by the host's key) to start from another.
  start: VariantDraft;
  definitions: HarnessSettingDefinition[];
  fleetVersions: FleetInstructionsVersion[];
  onSave: (draft: VariantDraft) => Promise<void>;
}

const NO_FLEET = "";

const HARNESS_OPTIONS: DropdownOption<VariantVersion["harness"]>[] = HARNESSES.map((h) => ({
  value: h.id,
  label: h.label,
}));

// VariantEditor is the form for the next version of a name: it never edits a
// saved one. The settings are harness config's own fields, for the chosen
// harness alone.
export default function VariantEditor({ start, definitions, fleetVersions, onSave }: VariantEditorProps) {
  const [name, setName] = useState(start.name);
  const [harness, setHarness] = useState(start.harness);
  const [fleetId, setFleetId] = useState<string | null>(start.fleetVersionId);
  const ids = useId();
  const harnessDefinitions = useMemo(() => definitions.filter((d) => d.harness === harness), [definitions, harness]);
  const fleetOptions = useMemo<DropdownOption[]>(
    () => [
      { value: NO_FLEET, label: "None" },
      ...fleetVersions
        .filter((f) => !f.archived || f.id === fleetId)
        .map((f) => ({ value: f.id, label: `${f.name} v${f.version}${f.archived ? " (archived)" : ""}` })),
    ],
    [fleetVersions, fleetId],
  );
  const saved = useMemo(() => ({ [start.harness]: start.settings }), [start]);

  const header = (
    <>
      <div className="variants__field">
        <label htmlFor={`${ids}-name`}>Name</label>
        <input id={`${ids}-name`} value={name} maxLength={64} onChange={(e) => setName(e.target.value)} />
        <span className="variants__hint">Saving under an existing name adds its next version.</span>
      </div>
      <div className="variants__field">
        <Dropdown label="Harness" value={harness} options={HARNESS_OPTIONS} onChange={(value) => setHarness(value)} />
      </div>
      <div className="variants__field">
        <Dropdown
          label="Fleet instructions"
          value={fleetId ?? NO_FLEET}
          options={fleetOptions}
          onChange={(value) => setFleetId(value || null)}
        />
        <span className="variants__hint">
          Pinned to this version. Agents on it get these, or none, never the tenant&rsquo;s default.
        </span>
      </div>
    </>
  );

  return (
    <HarnessConfigForm
      // A form keeps its fields' values: another harness is another form.
      key={harness}
      label="Save a variant version"
      definitions={harnessDefinitions}
      saved={harness === start.harness ? saved : undefined}
      header={header}
      canSubmit={name.trim() !== ""}
      allowUnchanged
      submitLabel="Save as a new version"
      onSave={async (settings) => {
        await onSave({ name: name.trim(), harness, settings: settings[harness] ?? {}, fleetVersionId: fleetId });
      }}
    />
  );
}
