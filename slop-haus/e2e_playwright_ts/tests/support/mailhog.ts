import { Mailhog } from "@e2e-support/mailhog";

import { MAILHOG_URL } from "@/support/env";

export { linkPath, tokenFrom } from "@e2e-support/mailhog";

const mailhog = new Mailhog(MAILHOG_URL);

export const clear = () => mailhog.clear();
export const waitForEmail = (to: string, subjectContains?: string, timeoutSeconds?: number) =>
  mailhog.waitForEmail(to, subjectContains, timeoutSeconds);
