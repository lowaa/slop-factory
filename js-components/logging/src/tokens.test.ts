import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { checkPackageTokens } from "@slop-factory/storybook-kit/design_tokens";
import { INTERNAL_TOKENS, REQUIRED_TOKENS } from "./tokens";

checkPackageTokens({
  srcDir: dirname(fileURLToPath(import.meta.url)),
  required: REQUIRED_TOKENS,
  internal: INTERNAL_TOKENS,
  mustFind: 2,
});
