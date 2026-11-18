import { resolve } from "node:path";

// Where the server reads the allowlist from (it embeds the file).
export const ALLOWLIST_PATH = resolve(import.meta.dirname, "../../../server/internal/usagemetrics/ui_allowlist.json");
