import { writeFileSync } from "node:fs";
import { allowlistJson } from "./allowlist_json.ts";
import { ALLOWLIST_PATH } from "./allowlist_path.ts";

writeFileSync(ALLOWLIST_PATH, allowlistJson());
