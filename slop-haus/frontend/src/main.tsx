import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.scss";
import App from "./app.tsx";
import { createSlopHausRouter } from "./routes.tsx";
import { ResourceProvider } from "./lib/resource";
import { createConsentStore } from "./privacy/consent_store.ts";

const router = createSlopHausRouter();
const consent = createConsentStore();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ResourceProvider>
      <App router={router} consent={consent} />
    </ResourceProvider>
  </StrictMode>,
);
