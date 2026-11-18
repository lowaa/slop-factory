import { Button } from "@slop-factory/ui";
import CopyButton from "@/shared/copy_button/copy_button";
import type { IntakeCredential } from "@/api";
import "./credential_box.scss";

interface CredentialBoxProps {
  credential: IntakeCredential;
  onDone: () => void;
}

// The one place a token or secret is rendered: the server answers it once,
// on create and rotate, and never again. It lives in the panel's state only
// until Done, and is not persisted anywhere.
export default function CredentialBox({ credential, onDone }: CredentialBoxProps) {
  return (
    <div className="credential-box" role="group" aria-label="New credential">
      <p className="credential-box__line">
        Source URL:{" "}
        <span className="credential-box__mono">{credential.source.url}</span>{" "}
        <CopyButton value={credential.source.url} label="source URL" />
      </p>
      <p className="credential-box__help">
        The sender POSTs its events to this URL.
      </p>
      {credential.token && (
        <>
          <p className="credential-box__line">
            Header:{" "}
            <span className="credential-box__mono">
              Authorization: Bearer {credential.token}
            </span>{" "}
            <CopyButton value={credential.token} label="token" />
          </p>
          <p className="credential-box__help">
            The sender sends the token with every request.
          </p>
        </>
      )}
      {credential.secret && (
        <>
          <p className="credential-box__line">
            Secret:{" "}
            <span className="credential-box__mono">{credential.secret}</span>{" "}
            <CopyButton value={credential.secret} label="secret" />
          </p>
          <p className="credential-box__help">
            The sender signs each request body with this secret.
          </p>
          {credential.signature_header && (
            <>
              <p className="credential-box__line">
                Signature header:{" "}
                <span className="credential-box__mono">
                  {credential.signature_header}
                </span>{" "}
                <CopyButton
                  value={credential.signature_header}
                  label="signature header"
                />
              </p>
              <p className="credential-box__help">
                The signature goes in this header, as sha256=&lt;hex&gt;.
              </p>
            </>
          )}
        </>
      )}
      <p className="credential-box__warning">
        The credential is shown only once -- if you lose it, rotate it to get
        a new one.
      </p>
      <Button onClick={onDone}>Done</Button>
    </div>
  );
}
