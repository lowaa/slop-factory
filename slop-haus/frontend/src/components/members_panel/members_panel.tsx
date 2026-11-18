import { useState, type FormEvent } from "react";
import { expiresIn } from "@/lib/format_time";
import { usePolledResource } from "@/lib/use_polled_resource";
import { useMembers } from "@/lib/shared_resources";
import { apiClient } from "@/api";
import "./members_panel.scss";
import { errorMessage } from "@/lib/use_async_action";

function expiresLabel(expiresAt: string): string {
  return expiresIn(expiresAt, Date.now(), "day");
}

export default function MembersPanel() {
  const members = useMembers({ fallbackError: "Failed to load members." });
  const invites = usePolledResource(() => apiClient.listInvites(), { fallbackError: "Failed to load invites." });
  const [email, setEmail] = useState("");
  const [inviting, setInviting] = useState(false);
  const [inviteError, setInviteError] = useState<string | null>(null);
  const [revokingId, setRevokingId] = useState<string | null>(null);
  const [revokeError, setRevokeError] = useState<string | null>(null);

  async function handleInvite(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setInviteError(null);
    setInviting(true);
    try {
      await apiClient.createInvite(email.trim());
      setEmail("");
      await invites.reload();
    } catch (err) {
      setInviteError(errorMessage(err, "Failed to send invite."));
    } finally {
      setInviting(false);
    }
  }

  async function handleRevoke(id: string) {
    setRevokeError(null);
    setRevokingId(id);
    try {
      await apiClient.revokeInvite(id);
      await invites.reload();
    } catch (err) {
      setRevokeError(errorMessage(err, "Failed to revoke invite."));
    } finally {
      setRevokingId(null);
    }
  }

  return (
    <div className="members-panel">
      <form className="members-panel__invite" onSubmit={handleInvite}>
        <h2>Invite someone</h2>
        <div className="members-panel__invite-row">
          <label className="members-panel__field">
            <span>Email</span>
            <input type="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
          </label>
          <button type="submit" disabled={inviting}>
            {inviting ? "Sending…" : "Send invite"}
          </button>
        </div>
        {inviteError && (
          <p className="members-panel__notice" role="alert">
            {inviteError}
          </p>
        )}
      </form>

      <section className="members-panel__section">
        <h2>Members</h2>
        {members.status === "loading" && <p>Loading members…</p>}
        {members.status === "error" && (
          <p role="alert" className="members-panel__notice">
            {members.error}
          </p>
        )}
        {members.status === "ready" && members.data && (
          <ul className="members-panel__list">
            {members.data.map((member) => (
              <li key={member.id} className="members-panel__row">
                <span className="members-panel__name">{member.display_name}</span>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="members-panel__section">
        <h2>Outstanding invites</h2>
        {revokeError && (
          <p role="alert" className="members-panel__notice">
            {revokeError}
          </p>
        )}
        {invites.status === "loading" && <p>Loading invites…</p>}
        {invites.status === "error" && (
          <p role="alert" className="members-panel__notice">
            {invites.error}
          </p>
        )}
        {invites.status === "ready" && invites.data && (
          <>
            <ul className="members-panel__list">
              {invites.data.map((invite) => (
                <li key={invite.id} className="members-panel__row">
                  <span className="members-panel__name">{invite.email}</span>
                  <span className="members-panel__expiry">{expiresLabel(invite.expires_at)}</span>
                  <button type="button" onClick={() => void handleRevoke(invite.id)} disabled={revokingId === invite.id}>
                    {revokingId === invite.id ? "Revoking…" : "Revoke"}
                  </button>
                </li>
              ))}
            </ul>
            {invites.data.length === 0 && <p className="members-panel__empty">No outstanding invites.</p>}
          </>
        )}
      </section>
    </div>
  );
}
