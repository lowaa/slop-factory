// Thin client for MailHog's HTTP API (https://github.com/mailhog/MailHog),
// built on the global fetch so tests need no HTTP client dependency.

interface MailhogAddress {
  Mailbox: string;
  Domain: string;
}

interface MailhogMessage {
  To: MailhogAddress[];
  Content: { Body: string; Headers?: Record<string, string[]> };
}

interface MailhogListResponse {
  items?: MailhogMessage[];
}

export interface MailMatch {
  to: string;
  subjectContains?: string;
}

export function matchesMail(message: MailhogMessage, match: MailMatch): boolean {
  const recipients = message.To.map((addr) => `${addr.Mailbox}@${addr.Domain}`);
  const subject = message.Content.Headers?.Subject?.[0] ?? "";
  return recipients.includes(match.to) && subject.includes(match.subjectContains ?? "");
}

export class Mailhog {
  constructor(
    private readonly baseUrl: string,
    private readonly pollMs = 300,
  ) {}

  // Deletes every captured message, so a test only sees mail sent during its
  // own run.
  async clear(): Promise<void> {
    const resp = await fetch(`${this.baseUrl}/api/v1/messages`, { method: "DELETE" });
    if (!resp.ok) {
      throw new Error(`failed to clear mailhog: status ${resp.status}`);
    }
  }

  // Polls until a message addressed to `to` shows up and returns its body,
  // which is the raw text as sent (the apps' mail has no MIME encoding beyond
  // its headers).
  //
  // `subjectContains` matters whenever one test sends an address more than
  // one kind of email -- register+activate then forgot-password, say. Without
  // it the wait for the second email returns on its first poll with the first
  // one, which is already in the inbox, and nothing later corrects it.
  async waitForEmail(to: string, subjectContains?: string, timeoutSeconds = 15): Promise<string> {
    const deadline = Date.now() + timeoutSeconds * 1000;
    let lastSeenCount = 0;
    while (Date.now() < deadline) {
      const resp = await fetch(`${this.baseUrl}/api/v2/messages?limit=50`);
      const items = ((await resp.json()) as MailhogListResponse).items ?? [];
      lastSeenCount = items.length;
      const found = items.find((item) => matchesMail(item, { to, subjectContains }));
      if (found) {
        return found.Content.Body;
      }
      await new Promise((resolve) => setTimeout(resolve, this.pollMs));
    }
    const subject = subjectContains === undefined ? "" : ` with subject containing "${subjectContains}"`;
    throw new Error(`no email to ${to}${subject} within ${timeoutSeconds}s (saw ${lastSeenCount} message(s) total)`);
  }
}

// The path plus fragment of the first link in an email body, e.g.
// "/activate#token=abc123" -- never its origin. The link's origin is the one
// the server was told to issue against, which need not be reachable from the
// test runner, so tests navigate the path against their own baseURL.
export function linkPath(body: string): string {
  const match = /https?:\/\/\S+/.exec(body);
  if (!match) {
    throw new Error(`no link found in email body: ${body}`);
  }
  const url = new URL(match[0]);
  return url.pathname + url.hash;
}

// The raw token from a path with a `#token=...` fragment, read the way the
// page that consumes it does. For a test that calls the token-consuming API
// directly instead of driving that page.
export function tokenFrom(path: string): string {
  const hash = path.split("#")[1] ?? "";
  const token = new URLSearchParams(hash).get("token");
  if (!token) {
    throw new Error(`no token in link path: ${path}`);
  }
  return token;
}

// The token of a body that is nothing but the link, for the apps whose mail
// is just the URL.
export function extractToken(body: string): string {
  return tokenFrom(linkPath(body.trim()));
}
