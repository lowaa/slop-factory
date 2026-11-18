import { useState } from "react";
import { useVariants, type VariantsData, type VariantsModel } from "./use_variants";
import { EMPTY_DRAFT, type VariantDraft } from "./variant_draft";
import VariantEditor from "./variant_editor";
import VariantHistory, { type VariantActions } from "./variant_history";
import "./variants.scss";

// VariantsWidget is where the tenant's owners define variants: a harness, its
// settings and a pinned fleet instructions version, for agents to run on. A
// version is never edited: saving adds the next one of its name. Authorisation
// is the server's: the list is refused to anyone else, and that is what hides
// the controls.
export default function VariantsWidget() {
  return <VariantsView model={useVariants()} />;
}

// VariantsView is the widget without its data source, so a host can supply a
// model of its own.
export function VariantsView({ model }: { model: VariantsModel }) {
  const { state, error } = model;
  return (
    <section className="variants" aria-label="Variants">
      <h3 className="variants__heading">Variants</h3>
      {state.status === "loading" && <p>Loading variants…</p>}
      {state.status === "error" && (
        <p role="alert" className="variants__error">
          {state.message}
        </p>
      )}
      {state.status === "forbidden" && (
        <p className="variants__hint">Only the tenant&rsquo;s owners can see and change variants.</p>
      )}
      {error && (
        <p role="alert" className="variants__error">
          {error}
        </p>
      )}
      {state.status === "loaded" && <Loaded data={state.data} model={model} />}
    </section>
  );
}

function Loaded({ data, model }: { data: VariantsData; model: VariantsModel }) {
  const [start, setStart] = useState<{ key: number; draft: VariantDraft }>({ key: 0, draft: EMPTY_DRAFT });

  const actions: VariantActions = {
    busy: model.busy,
    onStartFrom: (v) =>
      setStart((s) => ({
        key: s.key + 1,
        draft: { name: v.name, harness: v.harness, settings: v.settings, fleetVersionId: v.fleet_instructions_version_id },
      })),
    onArchive: (id) => void model.archiveVersion(id),
    onUnarchive: (id) => void model.unarchiveVersion(id),
    onArchiveGroup: (name) => void model.archiveGroup(name),
    onUnarchiveGroup: (name) => void model.unarchiveGroup(name),
  };

  async function save(draft: VariantDraft) {
    await model.save(draft.name, draft.harness, draft.settings, draft.fleetVersionId);
  }

  return (
    <>
      <p className="variants__hint">
        A variant is a harness, its settings and the fleet instructions it is given. An agent on one runs on those
        alone, ignoring the tenant&rsquo;s defaults and its own override, which apply again when it leaves. A saved
        version never changes; saving adds the next one of its name. Put an agent on a version from the Workers
        screen.
      </p>
      <label className="variants__checkbox">
        <input type="checkbox" checked={model.showArchived} onChange={(e) => model.setShowArchived(e.target.checked)} />
        Show archived
      </label>
      <VariantHistory
        versions={data.versions}
        definitions={data.definitions}
        fleetVersions={data.fleetVersions}
        actions={actions}
      />
      <VariantEditor
        key={start.key}
        start={start.draft}
        definitions={data.definitions}
        fleetVersions={data.fleetVersions}
        onSave={save}
      />
    </>
  );
}
