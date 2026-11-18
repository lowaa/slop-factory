import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/api";
import { checkRetry, CheckStepError, describeCheckFailure, runCheckStep } from "./session_check_failure";

beforeEach(() => {
  checkRetry.delayMs = 0;
});
afterEach(() => vi.restoreAllMocks());

describe("describeCheckFailure", () => {
  it("names the step and says offline when the browser is offline", () => {
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(false);
    expect(describeCheckFailure("list", new TypeError("Failed to fetch"))).toBe(
      "Could not list this machine's sessions: you appear to be offline.",
    );
  });
  it("says the request did not arrive for a network error while online", () => {
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(true);
    expect(describeCheckFailure("read", new TypeError("x"))).toBe(
      "Could not read the other session's transcript: the request did not reach the server.",
    );
  });
  it("explains a gateway status", () => {
    expect(describeCheckFailure("list", new ApiError(503, "Service Unavailable"))).toContain(
      "the server is unavailable or restarting (HTTP 503)",
    );
  });
  it("keeps the server's words and status for other API errors", () => {
    expect(describeCheckFailure("read", new ApiError(403, "not yours"))).toBe(
      "Could not read the other session's transcript: not yours (HTTP 403).",
    );
  });
  it("says the reply was not understood for anything else", () => {
    expect(describeCheckFailure("list", new SyntaxError("Unexpected token <"))).toContain("not understood");
  });
});

describe("runCheckStep", () => {
  it.each([
    ["network", () => new TypeError("x")],
    ["502", () => new ApiError(502, "bad")],
    ["503", () => new ApiError(503, "bad")],
    ["504", () => new ApiError(504, "bad")],
  ])("retries once after a %s failure", async (_n, make) => {
    const task = vi.fn().mockRejectedValueOnce(make()).mockResolvedValue("ok");
    await expect(runCheckStep("list", task)).resolves.toBe("ok");
    expect(task).toHaveBeenCalledTimes(2);
  });

  it("gives up after the second failure, tagged with its step", async () => {
    const task = vi.fn().mockRejectedValue(new TypeError("x"));
    await expect(runCheckStep("read", task)).rejects.toMatchObject({ step: "read" });
    expect(task).toHaveBeenCalledTimes(2);
  });

  it.each([new ApiError(500, "boom"), new ApiError(403, "no"), new SyntaxError("x")])(
    "does not retry %s",
    async (err) => {
      const task = vi.fn().mockRejectedValue(err);
      await expect(runCheckStep("list", task)).rejects.toBeInstanceOf(CheckStepError);
      expect(task).toHaveBeenCalledTimes(1);
    },
  );
});
