import { dirname, join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { compile } from "sass";
import { uiStylesImporter } from "@slop-factory/ui/styles/scss_breakpoints";
import { REQUIRED_TOKENS as AUTH } from "@slop-factory/auth/tokens";
import { REQUIRED_TOKENS as LOGGING } from "@slop-factory/logging/tokens";
import { REQUIRED_TOKENS as PRIVACY } from "@slop-factory/privacy/tokens";
import { REQUIRED_TOKENS as UI } from "@slop-factory/ui/tokens";
import { checkAppTokens } from "@slop-factory/storybook-kit/design_tokens";

const srcDir = join(dirname(fileURLToPath(import.meta.url)), "..");

checkAppTokens({
  srcDir,
  rootCss: compile(join(srcDir, "index.scss"), {
    style: "compressed",
    importers: [
      { findFileUrl: (url) => (url.startsWith("@/") ? pathToFileURL(join(srcDir, url.slice(2))) : null) },
      uiStylesImporter,
    ],
  }).css,
  packages: [
    { name: "auth", required: AUTH },
    { name: "logging", required: LOGGING },
    { name: "privacy", required: PRIVACY },
    { name: "ui", required: UI },
  ],
});
