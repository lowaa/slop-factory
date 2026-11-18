import { useState } from "react";
import { Button } from "@slop-factory/ui";
import type { FleetInstructions, FleetInstructionsKind } from "@/api";
import BriefingPreview from "./briefing_preview";
import VersionEditor, { type VersionDraft } from "./version_editor";
import VersionHistory, { type VersionActions } from "./version_history";
import { useFleetInstructions, type FleetInstructionsModel } from "./use_fleet_instructions";
import { KIND_COPY, KIND_ORDER } from "./kind_copy";
import WordListWarning from "./word_list_warning";
import "./fleet_instructions.scss";

const EMPTY_DRAFT: VersionDraft = { name: "", body: "", makeDefault: false };

// FleetInstructionsWidget is where the tenant's owners write the text put in
// every agent's briefing. A version is never edited: saving adds the next one
// of its name, and the default is a separate choice, so what the fleet is told
// changes only when an owner says so. Authorisation is the server's: can_edit
// only decides which controls to show. The text is an owner's own, shown as
// plain text.
export default function FleetInstructionsWidget() {
  const [kind, setKind] = useState<FleetInstructionsKind>("fleet");
  // Keyed by kind so a draft or an open history never carries over to another
  // kind's texts.
  return <KindPanel key={kind} kind={kind} onKind={setKind} />;
}

function KindPanel({ kind, onKind }: { kind: FleetInstructionsKind; onKind: (k: FleetInstructionsKind) => void }) {
  return <FleetInstructionsView model={useFleetInstructions(kind)} kind={kind} onKind={onKind} />;
}

// FleetInstructionsView is the widget without its data source, so a host can
// supply a model of its own.
export function FleetInstructionsView({
  model,
  kind = "fleet",
  onKind,
}: {
  model: FleetInstructionsModel;
  kind?: FleetInstructionsKind;
  onKind?: (kind: FleetInstructionsKind) => void;
}) {
  const { state, error } = model;
  return (
    <section className="fleet-instructions" aria-label="Fleet instructions">
      <h3 className="fleet-instructions__heading">{KIND_COPY[kind].heading}</h3>
      {onKind && (
        <nav aria-label="Kind of instructions" className="fleet-instructions__tabs">
          {KIND_ORDER.map((k) => (
            <Button
              key={k}
              size="sm"
              aria-current={k === kind ? "page" : undefined}
              onClick={() => onKind(k)}
            >
              {KIND_COPY[k].tab}
            </Button>
          ))}
        </nav>
      )}
      {state.status === "loading" && <p>Loading fleet instructions…</p>}
      {state.status === "error" && (
        <p role="alert" className="fleet-instructions__error">
          {state.message}
        </p>
      )}
      {error && (
        <p role="alert" className="fleet-instructions__error">
          {error}
        </p>
      )}
      {state.status === "loaded" && <Loaded data={state.data} model={model} kind={kind} />}
    </section>
  );
}

function Loaded({
  data,
  model,
  kind,
}: {
  data: FleetInstructions;
  model: FleetInstructionsModel;
  kind: FleetInstructionsKind;
}) {
  const copy = KIND_COPY[kind];
  const { busy } = model;
  const [draft, setDraft] = useState<VersionDraft>(EMPTY_DRAFT);

  const actions: VersionActions | undefined = data.can_edit
    ? {
        busy,
        onMakeDefault: (id) => void model.setDefault(id),
        onStartFrom: (v) => setDraft((d) => ({ ...d, name: v.name, body: v.body })),
        onArchive: (id) => void model.archiveVersion(id),
        onUnarchive: (id) => void model.unarchiveVersion(id),
        onArchiveGroup: (name) => void model.archiveGroup(name),
        onUnarchiveGroup: (name) => void model.unarchiveGroup(name),
      }
    : undefined;

  async function save() {
    if (await model.save(draft.name, draft.body, draft.makeDefault)) {
      setDraft((d) => ({ ...d, body: "", makeDefault: false }));
    }
  }

  return (
    <>
      <p className="fleet-instructions__hint">{copy.hint}</p>
      {kind === "sensitive_words" && <WordListWarning data={data} />}
      {kind === "fleet" && <BriefingPreview data={data} />}
      {data.default_id === null && (
        <p>
          {kind === "fleet"
            ? "No default is chosen, so agents\u2019 briefings carry no fleet instructions."
            : `No ${copy.heading.toLowerCase()} version is chosen.`}
        </p>
      )}
      {data.can_edit && data.default_id !== null && (
        <div className="fleet-instructions__actions">
          <Button disabled={busy} onClick={() => void model.setDefault(null)}>
            {kind === "fleet" ? "Use no fleet instructions" : "Use none"}
          </Button>
        </div>
      )}

      <label className="fleet-instructions__checkbox">
        <input
          type="checkbox"
          checked={model.showArchived}
          onChange={(e) => model.setShowArchived(e.target.checked)}
        />
        Show archived
      </label>
      <VersionHistory data={data} actions={actions} />

      {data.can_edit ? (
        <VersionEditor
          draft={draft}
          maxBodyBytes={data.max_body_bytes}
          busy={busy}
          onChange={setDraft}
          onSave={() => void save()}
          fieldLabel={copy.fieldLabel}
          allowEmpty={copy.allowEmpty}
        />
      ) : (
        <p className="fleet-instructions__hint">Only the tenant&rsquo;s owners can change {copy.heading.toLowerCase()}.</p>
      )}
    </>
  );
}
