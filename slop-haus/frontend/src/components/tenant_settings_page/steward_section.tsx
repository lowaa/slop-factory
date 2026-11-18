import { useId, useState } from "react";
import { Link } from "@slop-factory/router";
import { Button, Dropdown } from "@slop-factory/ui";
import { apiClient, type FleetInstructions,
  type Setting, type StewardDigest, type UpdateSettingInput } from "@/api";
import { setResource, resourceKeys } from "@/lib/resource";
import { usePolledResource } from "@/lib/use_polled_resource";
import TicketLink from "@/shared/ticket_link/ticket_link";
import { useTenantSettings } from "./use_tenant_settings";
import { useCurrentTenant } from "./use_current_tenant";
import { describeStewardModel, stewardModel } from "./steward_model";
import { describeStewardReport } from "@/components/agents_panel";
import SettingMinutesRow from "./setting_minutes_row";
import {
  DAILY_CAP_BOUNDS,
  levelChangeConfirmation,
  levelOf,
  parseTierOverrides,
  raisedOverrides,
  REVIEW_MAX_LINES_BOUNDS,
  settingNamed,
  STEWARD_CATEGORIES,
  STEWARD_CATEGORY_TIERS,
  STEWARD_DAILY_CAP,
  STEWARD_LEVEL,
  STEWARD_LEVELS,
  STEWARD_MODE,
  STEWARD_REVIEW_MAX_LINES,
  STEWARD_VETO_MINUTES,
  STEWARD_WARN_MIN_AGREEMENT_PCT,
  STEWARD_WARN_MIN_DECISIONS,
  stewardWarnings,
  tierAtLevel,
  tierChangeConfirmation,
  tiersApply,
  TIERS,
  TIERS_APPLY_FROM_LEVEL,
  VETO_MINUTES_BOUNDS,
  WARN_AGREEMENT_BOUNDS,
  WARN_DECISIONS_BOUNDS,
  agreementSummary,
  withTier,
  type CategoryRecord,
  type StewardCategory,
  type StewardLevel,
  type Tier,
  type TierOverrides,
} from "./steward_settings";
import { errorMessage } from "@/lib/use_async_action";

function errorText(err: unknown, fallback: string): string {
  return errorMessage(err, fallback);
}

// The section is the owner's: the server refuses anyone else's write, and
// showing the controls to a member would only invite a refusal.
export default function StewardSection() {
  const tenant = useCurrentTenant("Failed to load this tenant.");
  if (tenant.status !== "loaded" || !tenant.tenant.is_owner) return null;
  return <StewardControls />;
}

function StewardControls() {
  const settings = useTenantSettings();
  const [error, setError] = useState<string | null>(null);
  // Every tier edit rewrites the whole stored object from the confirmed one,
  // so edits wait their turn rather than overwrite each other.
  const [savingTiers, setSavingTiers] = useState(false);
  // A change that needs the owner's confirmation waits here until answered.
  const [pending, setPending] = useState<{
    text: string;
    apply: () => Promise<void>;
  } | null>(null);
  const digest = usePolledResource(() => apiClient.getStewardDigest("7d"), {
    fallbackError: "Failed to load the steward's agreement record.",
  });
  const workspace = usePolledResource(() => apiClient.getWorkspace(), {
    fallbackError: "Failed to load the workspace manifest.",
  });

  const harnessConfig = usePolledResource(() => apiClient.getHarnessConfig(), {
    fallbackError: "Failed to load the harness config.",
  });
  const agents = usePolledResource(() => apiClient.listAgents(), {
    fallbackError: "Failed to load the agents.",
  });

  const sensitiveWords = usePolledResource(() => apiClient.getFleetInstructions(false, "sensitive_words"), {
    fallbackError: "Failed to load the sensitive word list.",
  });

  async function write(name: string, input: UpdateSettingInput) {
    const written = await apiClient.updateSetting(name, input);
    await setResource<Setting[]>(resourceKeys.tenantSettings(), (current) =>
      (current ?? []).map((s) => (s.name === written.name ? written : s)),
    );
  }

  // Each control reports its own refusal beside itself; this is for the
  // category tiers, which share one stored value.
  async function writeTiers(category: StewardCategory, tier: Tier, current: string | null, levelDefault: Tier) {
    setError(null);
    setSavingTiers(true);
    try {
      const next = withTier(parseTierOverrides(current), category, tier, levelDefault);
      await write(STEWARD_CATEGORY_TIERS, {
        type: "string",
        string_val: JSON.stringify(next),
      });
    } catch (err: unknown) {
      setError(errorText(err, `Failed to change ${category.label}.`));
    } finally {
      setSavingTiers(false);
    }
  }

  const data = settings.data;
  const levelSetting = data && settingNamed(data, STEWARD_LEVEL);
  const mode = data && settingNamed(data, STEWARD_MODE);
  const warnDecisions = data && settingNamed(data, STEWARD_WARN_MIN_DECISIONS);
  const warnAgreement = data && settingNamed(data, STEWARD_WARN_MIN_AGREEMENT_PCT);
  const veto = data && settingNamed(data, STEWARD_VETO_MINUTES);
  const cap = data && settingNamed(data, STEWARD_DAILY_CAP);
  const tiers = data && settingNamed(data, STEWARD_CATEGORY_TIERS);
  const reviewMax = data && settingNamed(data, STEWARD_REVIEW_MAX_LINES);

  const level = levelOf(levelSetting, mode);
  const overrides = parseTierOverrides(tiers?.string_val ?? null);
  const stewardReport = describeStewardReport(agents.data?.find((a) => a.kind === "steward")?.observed);
  const records = recordsFrom(digest.data ?? null);
  const manifest = workspace.data?.manifest ?? null;
  const warnings = stewardWarnings({
    level,
    overrides,
    categories: STEWARD_CATEGORIES,
    record: (id) => records.get(id),
    minDecisions: warnDecisions?.int_val ?? WARN_DECISIONS_BOUNDS.fallback,
    minAgreementPct: warnAgreement?.int_val ?? WARN_AGREEMENT_BOUNDS.fallback,
    protectedPathRules:
      workspace.data === undefined
        ? null
        : (manifest?.repos ?? []).reduce((n, r) => n + (r.protected_paths?.length ?? 0), 0),
    sensitiveWordsEmpty: sensitiveWords.data === undefined ? null : sensitiveWordsEmpty(sensitiveWords.data),
  });

  // Confirmation is advice-shaped too: it names what changes and the owner can
  // still save, but never by accident.
  function confirmOr(text: string | null, apply: () => Promise<void>) {
    if (text) setPending({ text, apply });
    else void apply();
  }

  return (
    <section className="tenant-settings__section" aria-labelledby="steward-heading">
      <h2 id="steward-heading">Steward</h2>
      <p className="tenant-settings__muted">
        The steward decides which of your routine approvals an agent may take for you. Only you, as an owner, can change
        these.
      </p>
      <p className="tenant-settings__muted">
        Edit the <Link to="/workspace">steward instructions</Link> and the{" "}
        <Link to="/workspace">sensitive word list</Link> on the Workspace page.
      </p>
      {harnessConfig.data && agents.data && (
        <p className="tenant-settings__muted">
          {describeStewardModel(stewardModel(harnessConfig.data, agents.data))}. Set it in the steward's harness config
          on the <Link to="/workers">Workers page</Link>.
        </p>
      )}
      {stewardReport && (
        <p className="tenant-settings__muted" role="status">
          {stewardReport}
        </p>
      )}
      {warnings.length > 0 && (
        <ul className="steward-section__warnings" aria-label="Warnings">
          {warnings.map((w) => (
            <li key={w}>{w}</li>
          ))}
        </ul>
      )}
      {pending && (
        <div role="alertdialog" aria-label="Confirm steward change" className="steward-section__confirm">
          <p>{pending.text}</p>
          <Button
            variant="danger"
            onClick={() => {
              const { apply } = pending;
              setPending(null);
              void apply();
            }}
          >
            Save
          </Button>
          <Button onClick={() => setPending(null)}>Cancel</Button>
        </div>
      )}
      {data && (
        <ul className="tenant-settings__list">
          {(levelSetting || mode) && (
            <li className="tenant-settings__item">
              <LevelRow
                level={level}
                overrides={overrides}
                locked={pending !== null}
                confirmOr={confirmOr}
                onWrite={(next) => write(STEWARD_LEVEL, { type: "int", int_val: next })}
              />
            </li>
          )}
          {warnDecisions && (
            <li className="tenant-settings__item">
              <SettingMinutesRow
                key={warnDecisions.int_val}
                label="Warn below decisions"
                description="Warn when a category above human has fewer assessed decisions than this."
                value={warnDecisions.int_val ?? WARN_DECISIONS_BOUNDS.fallback}
                min={WARN_DECISIONS_BOUNDS.min}
                max={WARN_DECISIONS_BOUNDS.max}
                unit="decisions"
                onWrite={(next) =>
                  write(STEWARD_WARN_MIN_DECISIONS, {
                    type: "int",
                    int_val: next,
                  })
                }
              />
            </li>
          )}
          {warnAgreement && (
            <li className="tenant-settings__item">
              <SettingMinutesRow
                key={warnAgreement.int_val}
                label="Warn below agreement"
                description="Warn when your agreement with a category above human is lower than this."
                value={warnAgreement.int_val ?? WARN_AGREEMENT_BOUNDS.fallback}
                min={WARN_AGREEMENT_BOUNDS.min}
                max={WARN_AGREEMENT_BOUNDS.max}
                unit="%"
                onWrite={(next) =>
                  write(STEWARD_WARN_MIN_AGREEMENT_PCT, {
                    type: "int",
                    int_val: next,
                  })
                }
              />
            </li>
          )}
          {veto && (
            <li className="tenant-settings__item">
              <SettingMinutesRow
                key={veto.int_val}
                label="Veto window"
                description="How long a veto-tier decision waits for you before it is applied."
                value={veto.int_val ?? VETO_MINUTES_BOUNDS.fallback}
                min={VETO_MINUTES_BOUNDS.min}
                max={VETO_MINUTES_BOUNDS.max}
                onWrite={(next) => write(STEWARD_VETO_MINUTES, { type: "int", int_val: next })}
              />
            </li>
          )}
          {cap && (
            <li className="tenant-settings__item">
              <SettingMinutesRow
                key={cap.int_val}
                label="Daily cap"
                description="The most decisions the steward takes in a day."
                value={cap.int_val ?? DAILY_CAP_BOUNDS.fallback}
                min={DAILY_CAP_BOUNDS.min}
                max={DAILY_CAP_BOUNDS.max}
                unit="decisions"
                onWrite={(next) => write(STEWARD_DAILY_CAP, { type: "int", int_val: next })}
              />
            </li>
          )}
          {reviewMax && (
            <li className="tenant-settings__item">
              <SettingMinutesRow
                key={reviewMax.int_val}
                label="Review size limit"
                description="The largest change, in changed lines, whose review the steward may approve. Generated files don't count."
                value={reviewMax.int_val ?? REVIEW_MAX_LINES_BOUNDS.fallback}
                min={REVIEW_MAX_LINES_BOUNDS.min}
                max={REVIEW_MAX_LINES_BOUNDS.max}
                unit="lines"
                onWrite={(next) => write(STEWARD_REVIEW_MAX_LINES, { type: "int", int_val: next })}
              />
            </li>
          )}
          {tiers && (
            <li className="tenant-settings__item">
              <TierTable
                overrides={overrides}
                level={level}
                saving={savingTiers || pending !== null}
                digest={digest.data ?? null}
                records={records}
                onChange={(c, t) =>
                  confirmOr(tierChangeConfirmation(c, t), () =>
                    writeTiers(c, t, tiers.string_val, tierAtLevel(c, level, {})),
                  )
                }
                onClear={(c) => writeTiers(c, tierAtLevel(c, level, {}), tiers.string_val, tierAtLevel(c, level, {}))}
              />
              {error && (
                <p role="alert" className="tenant-settings__error">
                  {error}
                </p>
              )}
            </li>
          )}
        </ul>
      )}
    </section>
  );
}

function LevelRow({
  level,
  overrides,
  locked,
  confirmOr,
  onWrite,
}: {
  level: StewardLevel;
  overrides: TierOverrides;
  // A confirmation is open: answering it comes first, so two writes cannot race.
  locked: boolean;
  confirmOr: (text: string | null, apply: () => Promise<void>) => void;
  onWrite: (next: StewardLevel) => Promise<void>;
}) {
  const name = useId();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // The pending confirmation can be cancelled, so saving starts only once it
  // is answered.
  function choose(next: StewardLevel) {
    if (saving || next === level) return;
    confirmOr(levelChangeConfirmation(level, next, overrides), async () => {
      setSaving(true);
      setError(null);
      try {
        await onWrite(next);
      } catch (err: unknown) {
        setError(errorText(err, "Failed to change the steward's level."));
      } finally {
        setSaving(false);
      }
    });
  }

  return (
    <fieldset className="steward-section__modes" disabled={saving || locked}>
      <legend className="tenant-settings__name">Autonomy level</legend>
      {STEWARD_LEVELS.map((l) => (
        <label key={l.level} className="steward-section__mode">
          <input type="radio" name={name} checked={level === l.level} onChange={() => choose(l.level)} />
          <span>
            {l.level} {l.name}
          </span>
          <span className="tenant-settings__muted">
            {l.description} Example: {l.example}
          </span>
        </label>
      ))}
      {error && (
        <p role="alert" className="tenant-settings__error">
          {error}
        </p>
      )}
    </fieldset>
  );
}

// Empty means nothing is pinned, or the pinned version has no words.
function sensitiveWordsEmpty(list: FleetInstructions): boolean {
  const pinned = list.versions.find((v) => v.id === list.default_id);
  return !pinned || pinned.body.trim() === "";
}

// The 7-day digest's counts per category, as the owner's agreement record.
function recordsFrom(digest: StewardDigest | null): Map<string, CategoryRecord> {
  return new Map(
    (digest?.categories ?? []).map((c) => [
      c.category,
      {
        assessed: c.assessed,
        agreementPct: c.agreement_rate === null ? null : Math.round(c.agreement_rate * 100),
      },
    ]),
  );
}

const DISAGREEMENTS_SHOWN = 3;

function AgreementRecord({
  category,
  digest,
  record,
}: {
  category: StewardCategory;
  digest: StewardDigest | null;
  record?: CategoryRecord;
}) {
  if (!digest) return null;
  const latest = digest.decisions
    .filter((d) => d.category === category.id && (d.outcome === "vetoed" || d.outcome === "undone"))
    .slice(0, DISAGREEMENTS_SHOWN);
  return (
    <span className="tenant-settings__muted steward-section__record">
      {record
        ? `${record.assessed} decisions, ${record.agreementPct === null ? "no" : `${record.agreementPct}%`} agreement (7 days)`
        : "No decisions yet"}
      {latest.map((d) =>
        d.ticket_id === null ? null : (
          <span key={d.proposal_id}>
            {" "}
            <TicketLink ticketId={d.ticket_id} from="tenant_settings">#{d.ticket_id}</TicketLink> ({d.outcome})
          </span>
        ),
      )}
    </span>
  );
}

function TierTable({
  overrides,
  level,
  saving,
  digest,
  records,
  onChange,
  onClear,
}: {
  overrides: TierOverrides;
  level: StewardLevel;
  saving: boolean;
  digest: StewardDigest | null;
  records: Map<string, CategoryRecord>;
  onChange: (c: StewardCategory, t: Tier) => void;
  onClear: (c: StewardCategory) => void;
}) {
  const applies = tiersApply(level);
  const stored = new Map(raisedOverrides(overrides).map((o) => [o.category.id, o.tier]));
  return (
    <fieldset className="steward-section__tiers">
      <legend className="tenant-settings__name">Tier per category</legend>
      <p className="tenant-settings__muted">
        Auto is applied at once, veto after the window unless you veto it, human is always yours to decide. The level
        sets the starting tier; a change here overrides it for one category.
      </p>
      {!applies && (
        <p className="tenant-settings__muted">
          Tiers apply from level {TIERS_APPLY_FROM_LEVEL} (Routine). At this level every decision is yours.
        </p>
      )}
      {digest && (
        <p className="tenant-settings__muted" data-testid="steward-agreement-summary">
          {agreementSummary(digest.total)}
        </p>
      )}
      {STEWARD_CATEGORIES.map((c) => (
        <div key={c.id} className="steward-section__tier">
          <span aria-hidden="true">{c.label}</span>
          <AgreementRecord category={c} digest={digest} record={records.get(c.id)} />
          <Dropdown
            label={c.label}
            labelHidden
            value={tierAtLevel(c, level, overrides)}
            options={TIERS.map((t) => ({ value: t, label: t }))}
            disabled={c.neverAuto || saving || !applies}
            onChange={(t) => onChange(c, t)}
          />
          {c.neverAuto && <span className="tenant-settings__muted">Never automatic</span>}
          {!applies && stored.has(c.id) && (
            <>
              <span className="tenant-settings__muted">
                {stored.get(c.id)} (applies from level {TIERS_APPLY_FROM_LEVEL})
              </span>
              <Button disabled={saving} onClick={() => onClear(c)} aria-label={`Clear the stored tier for ${c.label}`}>
                Clear
              </Button>
            </>
          )}
        </div>
      ))}
    </fieldset>
  );
}
