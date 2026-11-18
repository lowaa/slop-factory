import { describe, expect, it } from "vitest";
import { ApiError } from "@/api";
import { reportErrorMessage } from "./report_error";

describe("reportErrorMessage", () => {
  const describe_ = reportErrorMessage("usage report", "Failed to load LLM usage.");

  it("advises a shorter period when the report timed out", () => {
    expect(describe_(new ApiError(503, "upstream"))).toBe("The usage report took too long to compute. Try a shorter period.");
  });

  it("passes on any other server message", () => {
    expect(describe_(new ApiError(500, "server said no"))).toBe("server said no");
  });

  it("falls back for anything that is not an ApiError", () => {
    expect(describe_(new Error("boom"))).toBe("Failed to load LLM usage.");
  });
});
