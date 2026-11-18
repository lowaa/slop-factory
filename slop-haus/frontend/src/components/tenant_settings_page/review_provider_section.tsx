import { useState } from "react";
import { Button, Dropdown } from "@slop-factory/ui";
import { apiClient, type Setting } from "@/api";
import { resourceKeys, setResource } from "@/lib/resource";
import { errorMessage, useAsyncAction } from "@/lib/use_async_action";
import { useCurrentTenant } from "./use_current_tenant";
import { useTenantSettings } from "./use_tenant_settings";
import {
  acceptedEfforts,
  defaultEffortLabel,
  DEFAULT_TRIGGER,
  describeChoice,
  EMPTY_CHOICE,
  parseStored,
  REVIEW_PROVIDER_CONFIG,
  REVIEW_PROVIDER_NAMES,
  REVIEW_PROVIDERS,
  serializeDraft,
  TRIGGER_CLASSES,
  validateDraft,
  type ChoiceDraft,
  type ProviderDraft,
} from "./review_provider_settings";
import "./review_provider_section.scss";

const MODEL_DEFAULT = "default";

interface ChoiceFieldsProps {
  name: string;
  choice: ChoiceDraft;
  onChange: (next: ChoiceDraft) => void;
  disabled: boolean;
  // A provider the other choice already uses, which this one may not repeat.
  excludeProvider?: string;
}

function ChoiceFields({ name, choice, onChange, disabled, excludeProvider }: ChoiceFieldsProps) {
  const efforts = acceptedEfforts(choice.provider);
  return (
    <div className="review-provider__fields">
      <Dropdown
        label={`${name} provider`}
        value={choice.provider === "" ? null : choice.provider}
        options={REVIEW_PROVIDER_NAMES.map((p) => ({ value: p, label: p, disabled: p === excludeProvider }))}
        disabled={disabled}
        onChange={(provider) =>
          // An effort one provider takes is not necessarily taken by the next.
          onChange({ ...choice, provider, effort: acceptedEfforts(provider).includes(choice.effort) ? choice.effort : "" })
        }
      />
      <label className="review-provider__field">
        <span>{name} model (optional)</span>
        <input
          type="text"
          value={choice.model}
          disabled={disabled}
          placeholder="the provider's default"
          autoComplete="off"
          onChange={(e) => onChange({ ...choice, model: e.target.value })}
        />
      </label>
      <Dropdown
        label={`${name} reasoning effort`}
        value={choice.effort === "" ? MODEL_DEFAULT : choice.effort}
        options={[{ value: MODEL_DEFAULT, label: defaultEffortLabel(choice.provider) }, ...efforts.map((e) => ({ value: e, label: e }))]}
        disabled={disabled || efforts.length === 0}
        onChange={(effort) => onChange({ ...choice, effort: effort === MODEL_DEFAULT ? "" : effort })}
      />
      {choice.provider in REVIEW_PROVIDERS && efforts.length === 0 && (
        <p className="tenant-settings__muted">{choice.provider} selects effort by model name, so there is none to set.</p>
      )}
    </div>
  );
}

interface FormProps {
  stored: string | null;
  onSave: (value: string) => Promise<void>;
}

function ProviderForm({ stored, onSave }: FormProps) {
  const { draft: initial, set, unreadable } = parseStored(stored);
  const [draft, setDraft] = useState<ProviderDraft>(initial);
  const [touched, setTouched] = useState(false);
  const action = useAsyncAction();
  const problems = validateDraft(draft);
  const value = serializeDraft(draft);
  // Compared as the form would store it, so a stored value the server wrote
  // in another key order or spacing is not shown as a change.
  const changed = unreadable || !set || value !== serializeDraft(initial);

  function edit(next: ProviderDraft) {
    setTouched(true);
    setDraft(next);
  }

  function save(to: string) {
    void action.run(() => onSave(to), (err) => errorMessage(err, "Failed to save the review provider."));
  }

  return (
    <form
      className="review-provider__form"
      onSubmit={(e) => {
        e.preventDefault();
        if (problems.length === 0) save(value);
      }}
    >
      {unreadable && (
        <p role="status" className="tenant-settings__muted">
          The stored value could not be read here; saving replaces it.
        </p>
      )}
      <fieldset className="review-provider__choice" disabled={action.busy}>
        <legend>Primary</legend>
        <ChoiceFields name="Primary" choice={draft.primary} disabled={action.busy} onChange={(primary) => edit({ ...draft, primary })} />
      </fieldset>
      <label className="review-provider__toggle">
        <input
          type="checkbox"
          checked={draft.fallback !== null}
          disabled={action.busy}
          onChange={(e) =>
            edit(
              e.target.checked
                ? { ...draft, fallback: EMPTY_CHOICE, fallbackOn: [...DEFAULT_TRIGGER] }
                : { ...draft, fallback: null, fallbackOn: [] },
            )
          }
        />
        <span>Use a fallback provider</span>
      </label>
      {draft.fallback && (
        <>
          <fieldset className="review-provider__choice" disabled={action.busy}>
            <legend>Fallback</legend>
            <ChoiceFields
              name="Fallback"
              choice={draft.fallback}
              disabled={action.busy}
              excludeProvider={draft.primary.provider}
              onChange={(fallback) => edit({ ...draft, fallback })}
            />
          </fieldset>
          <fieldset className="review-provider__choice" disabled={action.busy}>
            <legend>Fall back when the primary fails with</legend>
            {TRIGGER_CLASSES.map((c) => (
              <label key={c.id} className="review-provider__toggle">
                <input
                  type="checkbox"
                  checked={draft.fallbackOn.includes(c.id)}
                  onChange={(e) =>
                    edit({ ...draft, fallbackOn: e.target.checked ? [...draft.fallbackOn, c.id] : draft.fallbackOn.filter((x) => x !== c.id) })
                  }
                />
                <span>{c.label}</span>
              </label>
            ))}
          </fieldset>
        </>
      )}
      {touched && problems.length > 0 && (
        <ul aria-label="Problems" className="review-provider__problems">
          {problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      )}
      {action.error && (
        <p role="alert" className="review-provider__error">
          {action.error}
        </p>
      )}
      <div className="review-provider__actions">
        <Button type="submit" disabled={action.busy || problems.length > 0 || !changed}>
          Save
        </Button>
        {set && (
          <Button type="button" disabled={action.busy} onClick={() => save("")}>
            Clear
          </Button>
        )}
      </div>
    </form>
  );
}

function ReadOnlySummary({ stored }: { stored: string | null }) {
  const { draft, set, unreadable } = parseStored(stored);
  if (!set) return <p className="tenant-settings__value">None set: runs use each machine&apos;s local settings.</p>;
  if (unreadable) return <p className="tenant-settings__value">Set, but not readable here.</p>;
  return (
    <ul className="review-provider__summary">
      <li>Primary: {describeChoice(draft.primary)}</li>
      {draft.fallback && (
        <li>
          Fallback: {describeChoice(draft.fallback)}, on {draft.fallbackOn.join(", ")}
        </li>
      )}
    </ul>
  );
}

// Which provider, model and effort slop-review runs with, set once for the
// tenant. Every reviewed diff goes wherever this says, so it is the owner's
// to change; anyone else sees it read-only.
export default function ReviewProviderSection() {
  const settings = useTenantSettings();
  const tenant = useCurrentTenant("Failed to load this tenant.");
  const isOwner = tenant.status === "loaded" && tenant.tenant.is_owner;
  const setting = settings.data?.find((s) => s.name === REVIEW_PROVIDER_CONFIG);

  async function write(value: string) {
    const written = await apiClient.updateSetting(REVIEW_PROVIDER_CONFIG, { type: "string", string_val: value });
    await setResource<Setting[]>(resourceKeys.tenantSettings(), (current) =>
      (current ?? []).map((s) => (s.name === written.name ? written : s)),
    );
  }

  return (
    <section className="tenant-settings__section" aria-labelledby="review-provider-heading">
      <h2 id="review-provider-heading">Review provider</h2>
      <p className="tenant-settings__muted">
        Which provider, model and reasoning effort slop-review reviews with, for every machine in the tenant. Only an owner can change it: whatever is
        chosen here receives every reviewed diff. Providers are chosen by name; keys stay in each machine&apos;s environment.
      </p>
      {settings.status === "loading" && !settings.data && <p className="tenant-settings__muted">Loading…</p>}
      {/* A failed read is reported once, by the generic list above. */}
      {setting &&
        (isOwner ? (
          <ProviderForm key={setting.string_val ?? ""} stored={setting.string_val} onSave={write} />
        ) : (
          <>
            <ReadOnlySummary stored={setting.string_val} />
            <p className="tenant-settings__meta">Only an owner of the tenant can change this.</p>
          </>
        ))}
    </section>
  );
}
