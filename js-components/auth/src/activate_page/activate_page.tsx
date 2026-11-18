import { useEffect, useRef, useState } from "react";
import { useAuthPages } from "../config";
import AuthLayout, { AuthNotice } from "../auth_layout/auth_layout";
import { readAndClearToken } from "../token";

type ActivateState = { status: "activating" } | { status: "success" } | { status: "error"; message: string };

export default function ActivatePage() {
  const { api, navigate, routes, copy, describeError, onError } = useAuthPages();
  // Read (and scrub) the token during the initial render rather than from
  // inside the mount effect below -- deriving it here is what keeps a
  // set-state-in-effect lint rule quiet, and it's exactly what a lazy
  // useState initializer is for. React calls a lazy initializer twice in
  // development under StrictMode and keeps only the first result, so the
  // duplicate history.replaceState call below is harmless -- idempotent,
  // and the discarded second read (of an already-cleared hash) never
  // becomes state.
  const [token] = useState<string | null>(readAndClearToken);
  const [state, setState] = useState<ActivateState>(
    token ? { status: "activating" } : { status: "error", message: copy.missingActivationToken },
  );
  // Guards the POST itself: React's StrictMode double-invokes a mount
  // effect in development, and activation tokens are single-use, so a
  // naive effect would fail its own second call with a real error from the
  // server.
  const submitted = useRef(false);

  useEffect(() => {
    if (!token || submitted.current) return;
    submitted.current = true;

    api
      .activate(token)
      .then(() => setState({ status: "success" }))
      .catch((err: unknown) => {
        setState({ status: "error", message: describeError(err) });
        onError?.("activate", err);
      });
  }, [token, api, describeError, onError]);

  return (
    <AuthLayout title={copy.activateTitle}>
      {state.status === "activating" && <p>{copy.activating}</p>}

      {state.status !== "activating" && (
        <div className="auth-layout__body">
          {state.status === "success" && <AuthNotice variant="success">{copy.activated}</AuthNotice>}
          {state.status === "error" && <AuthNotice variant="error">{state.message}</AuthNotice>}
          <p className="auth-layout__switch">
            <a
              href={routes.login}
              onClick={(e) => {
                e.preventDefault();
                navigate(routes.login);
              }}
            >
              {copy.continueToSignIn}
            </a>
          </p>
        </div>
      )}
    </AuthLayout>
  );
}
