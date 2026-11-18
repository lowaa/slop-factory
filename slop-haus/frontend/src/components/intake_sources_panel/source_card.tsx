import type { ReactNode } from "react";
import { Button, Tooltip } from "@slop-factory/ui";
import type { IntakeSource } from "@/api";
import Timestamp from "@/shared/timestamp/timestamp";
import MemberName from "@/shared/names/member_name";
import SettingsSection from "./settings_section";
import { FIELD_DOCS, type FieldKey } from "./intake_field_docs";
import "./source_card.scss";

interface SourceCardProps {
  source: IntakeSource;
  busy: boolean;
  revoking: boolean;
  onRotate: () => void;
  onRevoke: () => void;
}

const ROTATE_HINT =
  "Issue a new credential and show it once. The old one stops working at once.";
const REVOKE_HINT =
  "Stop accepting deliveries from this source for good. It cannot be undone.";

function Setting({ field, children }: { field: FieldKey; children: ReactNode }) {
  return (
    <div className="source-card__setting">
      <dt>{FIELD_DOCS[field].label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

export default function SourceCard({
  source,
  busy,
  revoking,
  onRotate,
  onRevoke,
}: SourceCardProps) {
  const revoked = source.revoked_at !== null;
  return (
    <li className="source-card">
      <header className="source-card__header">
        <h4 className="source-card__name">{source.name}</h4>
        <span
          className={`source-card__trust source-card__trust--${source.trust}`}
        >
          {source.trust}
        </span>
        {revoked && <span className="source-card__revoked">Revoked</span>}
      </header>
      <p className="source-card__url">{source.url}</p>

      <div className="source-card__sections">
        <SettingsSection
        level={5}
          title="Authentication"
          summary="The credential is never shown again; rotate to get a new one."
        >
          <dl className="source-card__settings">
            <Setting field="auth">{source.auth}</Setting>
          </dl>
        </SettingsSection>
        <SettingsSection
        level={5}
          title="Mapping"
          summary="How what the sender posts becomes a ticket."
        >
          <dl className="source-card__settings">
            <Setting field="adapter">{source.adapter}</Setting>
            <Setting field="reopen_policy">
              {source.reopen_policy === "reopen"
                ? "reopen the ticket"
                : "file a new ticket"}
            </Setting>
            <Setting field="resolve_closes">
              {source.resolve_closes
                ? "cancel it if unclaimed"
                : "only add a comment"}
            </Setting>
          </dl>
        </SettingsSection>
        <SettingsSection
        level={5}
          title="Where tickets land"
          summary="What its tickets look like, and which agents may claim them."
        >
          <dl className="source-card__settings">
            <Setting field="default_status">{source.default_status}</Setting>
            <Setting field="default_priority">{source.default_priority}</Setting>
            <Setting field="max_priority">{source.max_priority}</Setting>
            <Setting field="roles">
              {source.roles.length > 0
                ? `${source.roles.join(", ")} (${source.role_match})`
                : "any agent"}
            </Setting>
            <Setting field="paths">
              {source.paths.length > 0 ? source.paths.join(", ") : "none"}
            </Setting>
          </dl>
        </SettingsSection>
      </div>

      <footer className="source-card__footer">
        <span className="source-card__meta">
          created <Timestamp at={source.created_at} />
          {(source.created_by || source.created_by_deleted) && (
            <>
              {" "}
              by <MemberName id={source.created_by} deleted={source.created_by_deleted} />
            </>
          )}
        </span>
        <span className="source-card__meta">
          last delivery{" "}
          {source.last_delivery_at ? (
            <Timestamp at={source.last_delivery_at} />
          ) : (
            "never"
          )}
        </span>
        {!revoked && (
          <div className="source-card__actions">
            <Tooltip text={ROTATE_HINT} hoverDelayMs={0}>
              <Button onClick={onRotate} disabled={busy || revoking}>
                Rotate
              </Button>
            </Tooltip>
            <Tooltip text={REVOKE_HINT} hoverDelayMs={0}>
              <Button variant="danger" onClick={onRevoke} disabled={revoking}>
                {revoking ? "Revoking…" : "Revoke"}
              </Button>
            </Tooltip>
          </div>
        )}
      </footer>
    </li>
  );
}
