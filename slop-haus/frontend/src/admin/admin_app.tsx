import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { AuthPagesProvider, AuthLayout, AuthNotice } from "@slop-factory/auth";
import { apiClient, type Invite } from "@/api";
import { AuthProvider } from "@/context/auth_context";
import { useAuth } from "@/context/auth";
import { useSlopHausAuthPagesConfig, type AuthPagesNavigation } from "@/auth_pages_config";
import { AUTH_ROUTES } from "@/auth_routes";
import "./admin_app.scss";
import { errorMessage } from "@/lib/use_async_action";

// AdminApp is deliberately small: it does one thing (invite people to start
// their own tenant) that only an admin can reach at all -- nginx's
// auth_request gate in front of /admin/ (see nginx.conf.template) means a
// non-admin's browser never even downloads this bundle's JS, so there is no
// board, no tickets here to duplicate slop-haus/frontend's main
// app. AuthProvider is shared with it, though (see its own file) -- it
// imports nothing board-specific, just apiClient and the Profile type, so
// reusing it here does not pull any of that in.
export default function AdminApp() {
  return (
    <AuthProvider>
      <AdminRoot />
    </AuthProvider>
  );
}

// This bundle has no pushState routing of its own -- /login lives in the
// main SPA bundle, a full page navigation away -- so the one config.navigate
// call that could ever fire here (there's no register/activate/reset link
// on this screen) just does that directly.
function navigateByFullPageLoad(path: string): void {
  window.location.href = path;
}

const FULL_PAGE_NAVIGATION: AuthPagesNavigation = { navigate: navigateByFullPageLoad, routes: AUTH_ROUTES };

function AdminRoot() {
  const { status, profile, signOut } = useAuth();
  const authPagesConfig = useSlopHausAuthPagesConfig(FULL_PAGE_NAVIGATION);

  let content: ReactNode;
  if (status === "loading") {
    content = <p className="admin-app__loading">Loading…</p>;
  } else if (status === "anonymous") {
    // Reached only if a session that was valid when nginx checked has
    // since expired -- the gate is enforced per request, not for the life
    // of this tab.
    content = (
      <AuthLayout title="Admin">
        <div className="auth-layout__body">
          <AuthNotice variant="info">Sign in to view the admin area.</AuthNotice>
          <p className="auth-layout__switch">
            <a href="/login">Sign in</a>
          </p>
        </div>
      </AuthLayout>
    );
  } else {
    content = (
      <div className="admin-app">
        <header className="admin-app__header">
          <h1>slop-haus admin</h1>
          <div className="admin-app__account">
            <span>{profile!.display_name}</span>
            <button type="button" onClick={() => void signOut()}>
              Sign out
            </button>
          </div>
        </header>
        <main className="admin-app__main">
          <AdminInvites />
        </main>
      </div>
    );
  }

  return <AuthPagesProvider config={authPagesConfig}>{content}</AuthPagesProvider>;
}

type InvitesState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; invites: Invite[] };

// AdminInvites invites people to start a tenant of their own -- distinct
// from members_panel.tsx's invites, which bring someone into the inviter's
// *existing* tenant (server/internal/user/invite.go's InviteToNewTenant vs
// InviteToTenant). Duplicating the shape of that component rather than
// sharing it: the two lists, endpoints and copy differ enough (no
// tenant_name here -- an admin invite has no tenant yet) that a shared
// component would need a kind flag threaded through anyway, and this bundle
// is meant to stay small and self-contained rather than reach back into the
// main app's component tree.
function AdminInvites() {
  const [state, setState] = useState<InvitesState>({ status: "loading" });
  const [email, setEmail] = useState("");
  const [inviting, setInviting] = useState(false);
  const [inviteError, setInviteError] = useState<string | null>(null);
  const [revokingId, setRevokingId] = useState<string | null>(null);
  const [revokeError, setRevokeError] = useState<string | null>(null);

  // issued/applied guards a slower, older fetch from landing after a newer
  // one and putting back stale data -- see members_panel.tsx's identical
  // guard (and member_name.tsx's own load(), the pattern both borrow) for
  // why: the mount fetch and a post-mutation refresh (sending an invite,
  // revoking one) can be in flight at once.
  const issued = useRef(0);
  const applied = useRef(0);

  const fetchInvites = useCallback(() => {
    const request = ++issued.current;
    return apiClient
      .listAdminInvites()
      .then((invites) => {
        if (request < applied.current) return;
        applied.current = request;
        setState({ status: "loaded", invites });
      })
      .catch((err: unknown) => {
        if (request < applied.current) return;
        applied.current = request;
        setState({ status: "error", message: errorMessage(err, "Failed to load invites.") });
      });
  }, []);

  useEffect(() => {
    fetchInvites();
  }, [fetchInvites]);

  async function handleInvite(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setInviteError(null);
    setInviting(true);
    try {
      await apiClient.createAdminInvite(email.trim());
      setEmail("");
      await fetchInvites();
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
      await apiClient.revokeAdminInvite(id);
      await fetchInvites();
    } catch (err) {
      setRevokeError(errorMessage(err, "Failed to revoke invite."));
    } finally {
      setRevokingId(null);
    }
  }

  return (
    <section className="admin-app__section">
      <h2>Invite someone to start a workspace</h2>
      <form className="admin-app__invite" onSubmit={handleInvite}>
        <label className="admin-app__field">
          <span>Email</span>
          <input type="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
        </label>
        <button type="submit" disabled={inviting}>
          {inviting ? "Sending…" : "Send invite"}
        </button>
      </form>
      {inviteError && (
        <p className="admin-app__notice" role="alert">
          {inviteError}
        </p>
      )}

      {revokeError && (
        <p role="alert" className="admin-app__notice">
          {revokeError}
        </p>
      )}

      {state.status === "loading" && <p>Loading invites…</p>}
      {state.status === "error" && (
        <p role="alert" className="admin-app__notice">
          {state.message}
        </p>
      )}
      {state.status === "loaded" && (
        <>
          <ul className="admin-app__list">
            {state.invites.map((invite) => (
              <li key={invite.id} className="admin-app__row">
                <span className="admin-app__email">{invite.email}</span>
                <button
                  type="button"
                  onClick={() => void handleRevoke(invite.id)}
                  disabled={revokingId === invite.id}
                >
                  {revokingId === invite.id ? "Revoking…" : "Revoke"}
                </button>
              </li>
            ))}
          </ul>
          {state.invites.length === 0 && <p className="admin-app__empty">No outstanding invites.</p>}
        </>
      )}
    </section>
  );
}
