import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";

import { expect, test } from "@playwright/test";

import { extractToken, linkPath, Mailhog, tokenFrom } from "./mailhog";

interface Sent {
  to: string;
  subject: string;
  body: string;
}

// A MailHog stand-in answering /api/v2/messages from `inbox`, which a test
// can add to while a wait is polling.
class FakeMailhog {
  readonly inbox: Sent[] = [];
  polls = 0;
  private readonly server: Server;

  constructor(private readonly onPoll: (polls: number) => void = () => {}) {
    this.server = createServer((req, res) => {
      res.setHeader("content-type", "application/json");
      if (req.url?.startsWith("/api/v2/messages")) {
        this.polls++;
        const items = this.inbox.map((m) => {
          const [mailbox, domain] = m.to.split("@");
          return { To: [{ Mailbox: mailbox, Domain: domain }], Content: { Body: m.body, Headers: { Subject: [m.subject] } } };
        });
        res.end(JSON.stringify({ items }));
        this.onPoll(this.polls);
      } else if (req.method === "DELETE") {
        this.inbox.length = 0;
        res.end("{}");
      } else {
        res.statusCode = 404;
        res.end();
      }
    });
  }

  async start(): Promise<string> {
    await new Promise<void>((resolve) => this.server.listen(0, "127.0.0.1", resolve));
    return `http://127.0.0.1:${(this.server.address() as AddressInfo).port}`;
  }

  stop(): Promise<void> {
    return new Promise((resolve) => this.server.close(() => resolve()));
  }
}

test.describe("waitForEmail", () => {
  let fake: FakeMailhog;
  let mailhog: Mailhog;

  test.beforeEach(async () => {
    fake = new FakeMailhog();
    mailhog = new Mailhog(await fake.start(), 5);
  });
  test.afterEach(() => fake.stop());

  test("picks the email whose subject matches when the address has received two kinds", async () => {
    fake.inbox.push({ to: "a@example.com", subject: "Activate your account", body: "activation" });
    fake.inbox.push({ to: "a@example.com", subject: "Reset your password", body: "reset" });
    expect(await mailhog.waitForEmail("a@example.com", "Reset your")).toBe("reset");
    expect(await mailhog.waitForEmail("a@example.com", "Activate your")).toBe("activation");
  });

  test("a stale email of another kind never satisfies the wait; the right one arriving later does", async () => {
    fake.inbox.push({ to: "a@example.com", subject: "Activate your account", body: "activation" });
    const waiting = mailhog.waitForEmail("a@example.com", "Reset your");
    // Delivered only after the first poll has seen the stale message alone.
    while (fake.polls < 1) await new Promise((r) => setTimeout(r, 1));
    fake.inbox.push({ to: "a@example.com", subject: "Reset your password", body: "reset" });
    expect(await waiting).toBe("reset");
  });

  test("without a subject, any email to the address matches", async () => {
    fake.inbox.push({ to: "a@example.com", subject: "anything", body: "first" });
    expect(await mailhog.waitForEmail("a@example.com")).toBe("first");
  });

  test("ignores mail to other addresses, and says what it saw when it gives up", async () => {
    fake.inbox.push({ to: "other@example.com", subject: "Reset your password", body: "not mine" });
    await expect(mailhog.waitForEmail("a@example.com", "Reset", 0.05)).rejects.toThrow(
      /no email to a@example.com with subject containing "Reset" within 0.05s \(saw 1 message\(s\) total\)/,
    );
  });

  test("clear empties the inbox", async () => {
    fake.inbox.push({ to: "a@example.com", subject: "s", body: "b" });
    await mailhog.clear();
    expect(fake.inbox).toEqual([]);
  });
});

test.describe("links", () => {
  test("linkPath keeps the path and fragment but drops the origin", () => {
    expect(linkPath("Hi\nhttp://localhost:5273/activate#token=abc%2B1\nbye")).toBe("/activate#token=abc%2B1");
  });

  test("linkPath refuses a body with no link", () => {
    expect(() => linkPath("no link here")).toThrow(/no link found/);
  });

  test("tokenFrom decodes the fragment token and refuses a path without one", () => {
    expect(tokenFrom("/activate#token=abc%2B1")).toBe("abc+1");
    expect(() => tokenFrom("/activate")).toThrow(/no token/);
  });

  test("extractToken reads a body that is just the link", () => {
    expect(extractToken("https://app.example/reset#token=xyz\n")).toBe("xyz");
  });
});
