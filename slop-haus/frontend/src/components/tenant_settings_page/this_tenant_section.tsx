import { useEffect, useRef, useState } from "react";
import { apiClient, TICKET_PROCESSING_ENABLED, type UpdateSettingInput, type Setting } from "@/api";
import { resourceKeys, setResource } from "@/lib/resource";
import { useTenantSettings } from "./use_tenant_settings";
import { announceProcessing, onProcessingAnnounced } from "@/components/processing_toggle";
import SettingHistory from "./setting_history";
import { STEWARD_SETTING_NAMES } from "./steward_settings";
import { REVIEW_PROVIDER_CONFIG } from "./review_provider_settings";
import { formatSettingValue } from "./setting_format";
import TenantNameRow from "./tenant_name_row";
import TenantColourRow from "./tenant_colour_row";
import SettingSwitchRow from "./setting_switch_row";
import MergeMethodRow, { MERGE_METHOD } from "./merge_method_row";
import SettingMinutesRow from "./setting_minutes_row";
import SettingNumberRow from "./setting_number_row";
import BatchingStatus from "./batching_status";
import { useCurrentTenant } from "./use_current_tenant";
import { labelFor, TENANT_SETTING_LABELS } from "./setting_labels";

export default function ThisTenantSection() {
  const settings = useTenantSettings();
  const tenant = useCurrentTenant("Failed to load this tenant.");
  const isOwner = tenant.status === "loaded" && tenant.tenant.is_owner;
  const [historyOpen, setHistoryOpen] = useState<ReadonlySet<string>>(new Set());
  const origin = useRef(Symbol("tenant_settings_page"));
  const { reload } = settings;

  // The header's switch changed it.
  useEffect(
    () =>
      onProcessingAnnounced((_enabled, from) => {
        if (from !== origin.current) void reload();
      }),
    [reload],
  );

  async function write(name: string, input: UpdateSettingInput) {
    const setting = await apiClient.updateSetting(name, input);
    await setResource<Setting[]>(resourceKeys.tenantSettings(), (current) =>
      (current ?? []).map((s) => (s.name === setting.name ? setting : s)),
    );
    if (setting.name === TICKET_PROCESSING_ENABLED && setting.bool_val !== null) {
      announceProcessing(setting.bool_val, origin.current);
    }
  }

  function toggleHistory(name: string) {
    setHistoryOpen((open) => {
      const next = new Set(open);
      if (!next.delete(name)) next.add(name);
      return next;
    });
  }

  return (
    <section className="tenant-settings__section" aria-labelledby="this-tenant-heading">
      <h2 id="this-tenant-heading">This tenant</h2>
      <p className="tenant-settings__muted">
        These apply to everyone in the tenant. Any member can change the settings listed here, except the ones marked owner-only; the name, the colour and
        the steward's settings are the owner's. Every change is recorded with who made it and when.
      </p>
      <TenantNameRow />
      <TenantColourRow />
      {settings.status === "loading" && <p className="tenant-settings__muted">Loading…</p>}
      {settings.status === "error" && !settings.data && (
        <p role="alert" className="tenant-settings__error">
          {settings.error}
        </p>
      )}
      {settings.data && (
        <ul className="tenant-settings__list">
          {settings.data
            .filter((setting) => !STEWARD_SETTING_NAMES.has(setting.name) && setting.name !== REVIEW_PROVIDER_CONFIG)
            .map((setting) => {
              const { label, description, minutes, number, link, ownerOnly, confirm } = labelFor(setting.name, TENANT_SETTING_LABELS);
              const open = historyOpen.has(setting.name);
              const meta = (
                <>
                  <p className="tenant-settings__meta">
                    {setting.is_default ? "Default value" : "Changed from its default"}
                  </p>
                  <div>
                    <button
                      type="button"
                      className="tenant-settings__link-button"
                      aria-expanded={open}
                      aria-label={`History of ${label}`}
                      onClick={() => toggleHistory(setting.name)}
                    >
                      {open ? "Hide history" : "History"}
                    </button>
                  </div>
                  {open && <SettingHistory name={setting.name} version={setting.updated_at} />}
                </>
              );
              return (
                <li key={setting.name} className="tenant-settings__item">
                  {setting.name === MERGE_METHOD ? (
                    <MergeMethodRow onWrite={(next) => write(setting.name, { type: "string", string_val: next })}>
                      {meta}
                    </MergeMethodRow>
                  ) : setting.type === "bool" ? (
                    <SettingSwitchRow
                      label={label}
                      description={description}
                      link={link}
                      confirm={confirm}
                      lockedNote={ownerOnly && !isOwner ? "Only an owner can change this." : undefined}
                      checked={setting.bool_val ?? false}
                      onWrite={(next) => write(setting.name, { type: "bool", bool_val: next })}
                    >
                      {meta}
                    </SettingSwitchRow>
                  ) : setting.type === "int" && minutes ? (
                    <SettingMinutesRow
                      key={setting.int_val}
                      label={label}
                      description={description}
                      value={setting.int_val ?? minutes.min}
                      min={minutes.min}
                      max={minutes.max}
                      onWrite={(next) => write(setting.name, { type: "int", int_val: next })}
                    >
                      {meta}
                    </SettingMinutesRow>
                  ) : setting.type === "int" && number ? (
                    <NumberSettingRow
                      key={setting.int_val}
                      ownerOnly={ownerOnly}
                      label={label}
                      description={description}
                      value={setting.int_val ?? number.min}
                      min={number.min}
                      max={number.max}
                      onWrite={(next) => write(setting.name, { type: "int", int_val: next })}
                    >
                      {setting.name === "merge_queue_batch_size" && <BatchingStatus batchSize={setting.int_val ?? number.min} />}
                      {meta}
                    </NumberSettingRow>
                  ) : (
                    <div>
                      <span className="tenant-settings__name">{label}</span>
                      <p className="tenant-settings__value">{formatSettingValue(setting)}</p>
                      {meta}
                    </div>
                  )}
                </li>
              );
            })}
        </ul>
      )}
    </section>
  );
}

type NumberSettingRowProps = React.ComponentProps<typeof SettingNumberRow> & { ownerOnly?: boolean };

// An owner-only setting is read-only until the tenant list says the member is
// an owner; the server refuses anyone else's write regardless. The tenant list
// is read here, not by the section, so a page with no owner-only number does
// not depend on it.
function NumberSettingRow({ ownerOnly, ...row }: NumberSettingRowProps) {
  const tenant = useCurrentTenant("Failed to load this tenant.");
  const isOwner = tenant.status === "loaded" && tenant.tenant.is_owner;
  const readOnlyNote = ownerOnly && !isOwner ? "Only an owner of the tenant can change this." : undefined;
  return <SettingNumberRow {...row} readOnlyNote={readOnlyNote} />;
}
