import type { FormEvent } from "react";
import { Button, Tooltip, type DropdownOption } from "@slop-factory/ui";
import IntakeField from "./intake_field";
import IntakeSelect from "./intake_select";
import SettingsSection from "./settings_section";
import { PRIORITIES, splitList, type FormValues } from "./intake_form_values";
import "./new_source_form.scss";

interface NewSourceFormProps {
  form: FormValues;
  onChange: <K extends keyof FormValues>(key: K, value: FormValues[K]) => void;
  onSubmit: () => void;
  // enabled: the server has an intake door; hmac: it has an INTAKE_KEY.
  enabled: boolean;
  hmac: boolean;
  busy: boolean;
}

const PRIORITY_OPTIONS: DropdownOption<FormValues["default_priority"]>[] =
  PRIORITIES.map((p) => ({
    value: p,
    label: p,
  }));

const TRUST_OPTIONS: DropdownOption<FormValues["trust"]>[] = [
  { value: "untrusted", label: "untrusted" },
  { value: "trusted", label: "trusted" },
];

const ADAPTER_OPTIONS: DropdownOption<FormValues["adapter"]>[] = [
  { value: "native", label: "native" },
  { value: "alertmanager", label: "alertmanager" },
];

const REOPEN_OPTIONS: DropdownOption<FormValues["reopen_policy"]>[] = [
  { value: "new", label: "file a new ticket" },
  { value: "reopen", label: "reopen the ticket" },
];

const RESOLVE_CLOSES_OPTIONS: DropdownOption<FormValues["resolve_closes"]>[] = [
  { value: "off", label: "only add a comment" },
  { value: "on", label: "cancel it if unclaimed" },
];

const STATUS_OPTIONS: DropdownOption<FormValues["default_status"]>[] = [
  { value: "open", label: "open" },
  { value: "preparing", label: "preparing" },
];

const ROLE_MATCH_OPTIONS: DropdownOption<FormValues["role_match"]>[] = [
  { value: "any", label: "any" },
  { value: "all", label: "all" },
];

export default function NewSourceForm({
  form,
  onChange,
  onSubmit,
  enabled,
  hmac,
  busy,
}: NewSourceFormProps) {
  const untrusted = form.trust === "untrusted";
  const authOptions: DropdownOption<FormValues["auth"]>[] = [
    { value: "bearer", label: "bearer" },
    { value: "hmac", label: "hmac", disabled: !hmac },
  ];
  const hasRoles = splitList(form.roles).length > 0;

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    onSubmit();
  }

  return (
    <form
      className="new-source-form"
      onSubmit={handleSubmit}
      aria-label="New intake source"
    >
      <SettingsSection
        level={3}
        title="Sender"
        summary="Who is sending, and how far its tickets are trusted."
      >
        <IntakeField field="name">
          {(id) => (
            <input
              id={id}
              type="text"
              required
              value={form.name}
              onChange={(e) => onChange("name", e.target.value)}
            />
          )}
        </IntakeField>
        <IntakeSelect
          field="trust"
          value={form.trust}
          options={TRUST_OPTIONS}
          onChange={(v) => onChange("trust", v)}
        />
      </SettingsSection>

      <SettingsSection
        level={3}
        title="Authentication"
        summary="How the sender proves a request is from this source."
      >
        <IntakeSelect
          field="auth"
          value={form.auth}
          options={authOptions}
          onChange={(v) => onChange("auth", v)}
          hint={hmac ? undefined : "HMAC needs an INTAKE_KEY on the server."}
        />
      </SettingsSection>

      <SettingsSection
        level={3}
        title="Mapping"
        summary="How what the sender posts becomes a ticket."
      >
        <IntakeSelect
          field="adapter"
          value={form.adapter}
          options={ADAPTER_OPTIONS}
          onChange={(v) => onChange("adapter", v)}
        />
        <IntakeSelect
          field="reopen_policy"
          value={form.reopen_policy}
          options={REOPEN_OPTIONS}
          onChange={(v) => onChange("reopen_policy", v)}
        />
        <IntakeSelect
          field="resolve_closes"
          value={untrusted ? "off" : form.resolve_closes}
          options={RESOLVE_CLOSES_OPTIONS}
          disabled={untrusted}
          onChange={(v) => onChange("resolve_closes", v)}
          hint={
            untrusted
              ? "An untrusted source cannot close tickets."
              : undefined
          }
        />
      </SettingsSection>

      <SettingsSection
        level={3}
        title="Where tickets land"
        summary="What its tickets look like, and which agents may claim them."
      >
        <IntakeSelect
          field="default_status"
          value={untrusted ? "preparing" : form.default_status}
          options={STATUS_OPTIONS}
          disabled={untrusted}
          onChange={(v) => onChange("default_status", v)}
          hint={
            untrusted
              ? "An untrusted source's tickets are always Preparing."
              : undefined
          }
        />
        <IntakeSelect
          field="default_priority"
          value={form.default_priority}
          options={PRIORITY_OPTIONS}
          onChange={(v) => onChange("default_priority", v)}
        />
        <IntakeSelect
          field="max_priority"
          value={form.max_priority}
          options={PRIORITY_OPTIONS}
          onChange={(v) => onChange("max_priority", v)}
        />
        <IntakeField field="roles">
          {(id) => (
            <input
              id={id}
              type="text"
              value={form.roles}
              onChange={(e) => onChange("roles", e.target.value)}
            />
          )}
        </IntakeField>
        {hasRoles && (
          <IntakeSelect
            field="role_match"
            value={form.role_match}
            options={ROLE_MATCH_OPTIONS}
            onChange={(v) => onChange("role_match", v)}
          />
        )}
        <IntakeField field="paths">
          {(id) => (
            <input
              id={id}
              type="text"
              value={form.paths}
              onChange={(e) => onChange("paths", e.target.value)}
            />
          )}
        </IntakeField>
      </SettingsSection>

      <div className="new-source-form__actions">
        <Tooltip
          text="Registers the source and shows its credential once."
          hoverDelayMs={0}
        >
          <Button type="submit" variant="primary" disabled={!enabled || busy}>
            {busy ? "Creating…" : "Create source"}
          </Button>
        </Tooltip>
      </div>
    </form>
  );
}
