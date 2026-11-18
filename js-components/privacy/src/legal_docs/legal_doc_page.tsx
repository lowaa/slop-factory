import { useEffect, useState } from "react";
import LegalDocumentView from "./legal_document";
import { NotFoundError } from "./fetch_api";
import type { LegalDocHref, LegalDocsApi, LegalDocument, LegalHistory } from "./types";

interface LegalDocPageProps {
  api: LegalDocsApi;
  kind: string;
  // Undefined is the version in force.
  version?: string;
  href: LegalDocHref;
}

type State =
  | { status: "loading" }
  | { status: "missing" }
  | { status: "error" }
  | { status: "ready"; document: LegalDocument; history: LegalHistory | null };

// Loads and renders a legal document. The history is a nicety: the page
// shows the text without it if only that request fails.
export default function LegalDocPage({ api, kind, version, href }: LegalDocPageProps) {
  const [state, setState] = useState<State>({ status: "loading" });

  useEffect(() => {
    let live = true;
    setState({ status: "loading" });
    Promise.all([api.getDocument(kind, version), api.getHistory(kind).catch(() => null)]).then(
      ([document, history]) => live && setState({ status: "ready", document, history }),
      (err: unknown) => live && setState({ status: err instanceof NotFoundError ? "missing" : "error" }),
    );
    return () => {
      live = false;
    };
  }, [api, kind, version]);

  switch (state.status) {
    case "loading":
      return <p role="status">Loading…</p>;
    case "missing":
      return <p role="alert">This document is not available.</p>;
    case "error":
      return <p role="alert">This document could not be loaded. Try again later.</p>;
    case "ready":
      return <LegalDocumentView document={state.document} history={state.history} href={href} />;
  }
}
