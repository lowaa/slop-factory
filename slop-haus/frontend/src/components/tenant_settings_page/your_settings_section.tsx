import { useEffect, useState } from "react";
import { apiClient, type TenantUserSetting } from "@/api";
import { RENDER_MARKDOWN, useRenderMarkdown } from "@/lib/markdown_setting";
import { UserSettingsProvider } from "@/lib/user_settings_provider";
import SettingHistory, { loadMyTenantHistory } from "./setting_history";
import SettingSwitchRow from "./setting_switch_row";
import { labelFor, OTHER_GROUP_TITLE, YOUR_SETTING_GROUPS, type SettingLabel } from "./setting_labels";
import { errorMessage } from "@/lib/use_async_action";

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; settings: TenantUserSetting[] };

interface Group {
  title: string;
  intro?: string;
  labels: Record<string, SettingLabel>;
  settings: TenantUserSetting[];
}

// Labelled groups first, in the map's order, then whatever the map does not
// know under "Other" so a new setting shows up before it has a label.
function groupSettings(settings: TenantUserSetting[]): Group[] {
  const known = new Set(YOUR_SETTING_GROUPS.flatMap((g) => Object.keys(g.names)));
  const groups: Group[] = YOUR_SETTING_GROUPS.map((g) => ({
    title: g.title,
    intro: g.intro,
    labels: g.names,
    settings: settings.filter((s) => s.name in g.names),
  })).filter((g) => g.settings.length > 0);
  const other = settings.filter((s) => !known.has(s.name));
  if (other.length > 0) groups.push({ title: OTHER_GROUP_TITLE, labels: {}, settings: other });
  return groups;
}

// A setting that follows the person into every tenant, so it is read and
// written through the person's own settings rather than this tenant's.
function RenderMarkdownRow() {
  const markdown = useRenderMarkdown();
  return (
    <SettingSwitchRow
      label="Render markdown"
      description="Show ticket text and Remote Control messages as formatted markdown. When off, they show as plain text. This applies to you in every tenant."
      checked={markdown.saved}
      onWrite={async (next) => markdown.change(next)}
    >
      {markdown.error && (
        <p role="alert" className="tenant-settings__error">
          {markdown.error}
        </p>
      )}
    </SettingSwitchRow>
  );
}

export default function YourSettingsSection() {
  const [state, setState] = useState<State>({ status: "loading" });
  const [historyOpen, setHistoryOpen] = useState<Set<string>>(new Set());

  function toggleHistory(name: string) {
    setHistoryOpen((open) => {
      const next = new Set(open);
      if (!next.delete(name)) next.add(name);
      return next;
    });
  }

  useEffect(() => {
    let current = true;
    apiClient
      .listMyTenantSettings()
      .then((settings) => {
        if (current) setState({ status: "loaded", settings });
      })
      .catch((err: unknown) => {
        if (!current) return;
        setState({ status: "error", message: errorMessage(err, "Failed to load your settings.") });
      });
    return () => {
      current = false;
    };
  }, []);

  async function write(name: string, next: boolean) {
    const written = await apiClient.setMyTenantBoolSetting(name, next);
    setState((current) =>
      current.status === "loaded"
        ? { status: "loaded", settings: current.settings.map((s) => (s.name === name ? written : s)) }
        : current,
    );
  }

  return (
    <section className="tenant-settings__section" aria-labelledby="your-settings-heading">
      <h2 id="your-settings-heading">You in this tenant</h2>
      <p className="tenant-settings__muted">
        Your own settings, which apply only here and only to you. Other members keep their own.
      </p>
      <div role="group" aria-label="Display" className="tenant-settings__group">
        <h3>Display</h3>
        <ul className="tenant-settings__list">
          <li key={RENDER_MARKDOWN} className="tenant-settings__item">
            <UserSettingsProvider>
              <RenderMarkdownRow />
            </UserSettingsProvider>
          </li>
        </ul>
      </div>
      {state.status === "loading" && <p className="tenant-settings__muted">Loading…</p>}
      {state.status === "error" && (
        <p role="alert" className="tenant-settings__error">
          {state.message}
        </p>
      )}
      {state.status === "loaded" && state.settings.length === 0 && (
        <p className="tenant-settings__muted">You have no settings in this tenant.</p>
      )}
      {state.status === "loaded" &&
        groupSettings(state.settings).map((group) => (
          <div key={group.title} role="group" aria-label={group.title} className="tenant-settings__group">
            <h3>{group.title}</h3>
            {group.intro && <p className="tenant-settings__muted">{group.intro}</p>}
            <ul className="tenant-settings__list">
              {group.settings.map((setting) => {
                const { label, description } = labelFor(setting.name, group.labels);
                const open = historyOpen.has(setting.name);
                const history = (
                  <>
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
                    {open && (
                      <SettingHistory
                        name={setting.name}
                        load={loadMyTenantHistory(setting.name)}
                        previousAsFrom
                        emptyText="No changes yet"
                        version={setting.updated_at}
                      />
                    )}
                  </>
                );
                return (
                  <li key={setting.name} className="tenant-settings__item">
                    {setting.type === "bool" ? (
                      <SettingSwitchRow
                        label={label}
                        description={description}
                        checked={setting.bool_val ?? false}
                        onWrite={(next) => write(setting.name, next)}
                      >
                        {history}
                      </SettingSwitchRow>
                    ) : (
                      <div>
                        <span className="tenant-settings__name">{label}</span>
                        <p className="tenant-settings__value">
                          {setting.str_val ?? setting.int_val ?? setting.float_val}
                        </p>
                        {history}
                      </div>
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
    </section>
  );
}
