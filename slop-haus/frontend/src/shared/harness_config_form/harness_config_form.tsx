import { useId, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { Button, Dropdown, type DropdownOption } from "@slop-factory/ui";
import { type HarnessSettingDefinition, type HarnessSettings } from "@/api";
import { HARNESS_LABELS } from "@/domain/harnesses";
import "./harness_config_form.scss";
import { errorMessage } from "@/lib/use_async_action";

// Suggestions only: any model id the harness takes is allowed, as long as
// it passes the server's pattern.
const MODEL_SUGGESTIONS = ["sonnet", "opus", "haiku", "fable"];


type Values = Record<string, string>;

function key(d: HarnessSettingDefinition): string {
  return `${d.harness}.${d.name}`;
}

function toValues(definitions: HarnessSettingDefinition[], settings: HarnessSettings | undefined): Values {
  const out: Values = {};
  for (const d of definitions) out[key(d)] = settings?.[d.harness]?.[d.name] ?? "";
  return out;
}

// toSettings is what a save sends: the whole scope, with every empty field
// left out, since the server reads an absent setting as unset.
function toSettings(definitions: HarnessSettingDefinition[], values: Values): HarnessSettings {
  const out: HarnessSettings = {};
  for (const d of definitions) {
    const value = values[key(d)];
    if (!value) continue;
    (out[d.harness] ??= {})[d.name] = value;
  }
  return out;
}

// invalid says why a value would be refused, or null. The server checks
// again on save with the same pattern; this is only so the person finds
// out while typing.
function invalid(d: HarnessSettingDefinition, value: string): string | null {
  if (!value) return null;
  if (d.kind === "enum") return d.options.includes(value) ? null : "not one of the options";
  if (d.pattern && !new RegExp(d.pattern).test(value)) {
    return "letters, digits and . _ : / [ ] -, starting with a letter or digit";
  }
  return null;
}

// groupByHarness keeps the server's order, both of harnesses and of the
// settings within each.
function enumOptions(options: string[], fallback: string | undefined): DropdownOption[] {
  return [
    { value: "", label: fallback ? `default (${fallback})` : "not set" },
    ...options.map((option) => ({ value: option, label: option })),
  ];
}

function groupByHarness(definitions: HarnessSettingDefinition[]): [string, HarnessSettingDefinition[]][] {
  const groups = new Map<string, HarnessSettingDefinition[]>();
  for (const d of definitions) {
    if (!groups.has(d.harness)) groups.set(d.harness, []);
    groups.get(d.harness)!.push(d);
  }
  return Array.from(groups);
}

interface HarnessConfigFormProps {
  definitions: HarnessSettingDefinition[];
  // saved is this scope's own settings as last saved.
  saved: HarnessSettings | undefined;
  // inherited, for an agent's override, is what it gets where the
  // override sets nothing: shown in each empty field so "leave it empty"
  // visibly means "take the default".
  inherited?: HarnessSettings;
  onSave: (settings: HarnessSettings) => Promise<void>;
  onCancel?: () => void;
  label: string;
  // Fields of the host's own, drawn above the settings (a variant's name and
  // harness), and the host's say on whether the form may be submitted. A
  // host that creates something new rather than editing a scope also sets
  // allowUnchanged, since the form is complete before anything is typed.
  header?: ReactNode;
  canSubmit?: boolean;
  allowUnchanged?: boolean;
  submitLabel?: string;
}

export default function HarnessConfigForm({
  definitions,
  saved,
  inherited,
  onSave,
  onCancel,
  label,
  header,
  canSubmit = true,
  allowUnchanged = false,
  submitLabel = "Save",
}: HarnessConfigFormProps) {
  const initial = useMemo(() => toValues(definitions, saved), [definitions, saved]);
  const [values, setValues] = useState<Values>(initial);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const suggestionsId = useId();

  const problems = definitions.map((d) => invalid(d, values[key(d)] ?? ""));
  const dirty = definitions.some((d) => (values[key(d)] ?? "") !== initial[key(d)]);
  const canSave = (dirty || allowUnchanged) && canSubmit && !saving && problems.every((p) => p === null);

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!canSave) return;
    setSaving(true);
    setError(null);
    try {
      await onSave(toSettings(definitions, values));
    } catch (err) {
      setError(errorMessage(err, "Failed to save the harness config."));
    } finally {
      setSaving(false);
    }
  }

  return (
    <form className="harness-config-form" aria-label={label} onSubmit={(e) => void handleSubmit(e)}>
      {header}
      <datalist id={suggestionsId}>
        {MODEL_SUGGESTIONS.map((m) => (
          <option key={m} value={m} />
        ))}
      </datalist>
      {groupByHarness(definitions).map(([harness, settings]) => (
        <fieldset key={harness} className="harness-config-form__harness">
          <legend>{HARNESS_LABELS[harness] ?? harness}</legend>
          {settings.map((d) => {
            const id = `${suggestionsId}-${key(d)}`;
            const value = values[key(d)] ?? "";
            const fallback = inherited?.[d.harness]?.[d.name];
            const problem = problems[definitions.indexOf(d)];
            const set = (next: string) => setValues((prev) => ({ ...prev, [key(d)]: next }));
            return (
              <div key={key(d)} className="harness-config-form__field">
                {d.kind !== "enum" && <label htmlFor={id}>{d.label}</label>}
                {d.kind === "enum" ? (
                  <Dropdown
                    label={d.label}
                    value={value}
                    options={enumOptions(d.options, fallback)}
                    onChange={set}
                    disabled={saving}
                  />
                ) : (
                  <input
                    id={id}
                    type="text"
                    value={value}
                    list={d.kind === "model" ? suggestionsId : undefined}
                    placeholder={fallback ? `default (${fallback})` : "not set"}
                    onChange={(e) => set(e.target.value.trim())}
                    aria-invalid={problem !== null}
                    disabled={saving}
                    spellCheck={false}
                    autoComplete="off"
                  />
                )}
                <span className="harness-config-form__help">{problem ?? d.help}</span>
              </div>
            );
          })}
        </fieldset>
      ))}
      {error && (
        <p role="alert" className="harness-config-form__error">
          {error}
        </p>
      )}
      <div className="harness-config-form__actions">
        <Button type="submit" variant="primary" disabled={!canSave}>
          {saving ? "Saving…" : submitLabel}
        </Button>
        {onCancel && (
          <Button variant="ghost" onClick={onCancel} disabled={saving}>
            Cancel
          </Button>
        )}
      </div>
    </form>
  );
}
