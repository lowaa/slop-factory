import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
// Shared global tokens/resets --
// same file the main bundle's main.tsx imports, so the admin pages render
// with the same dark theme rather than unstyled or duplicated variables.
import "@/index.scss";
import AdminApp from "./admin_app.tsx";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AdminApp />
  </StrictMode>,
);
