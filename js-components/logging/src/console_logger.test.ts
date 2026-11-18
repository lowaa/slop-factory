import { describe, expect, it, vi } from "vitest";
import { ConsoleLogger } from "./console_logger";

describe("ConsoleLogger", () => {
  it.each(["debug", "info", "warn", "error"] as const)(
    "%s delegates to console.%s, attrs included only when given",
    (level) => {
      const spy = vi.spyOn(console, level).mockImplementation(() => {});
      const logger = new ConsoleLogger();

      logger[level]("plain message");
      logger[level]("with attrs", { key: "value" });

      expect(spy).toHaveBeenNthCalledWith(1, "plain message");
      expect(spy).toHaveBeenNthCalledWith(2, "with attrs", { key: "value" });
      spy.mockRestore();
    },
  );

  it("event logs the name at info level", () => {
    const spy = vi.spyOn(console, "info").mockImplementation(() => {});

    new ConsoleLogger().event("clicked", { a: 1 });

    expect(spy).toHaveBeenCalledWith("event: clicked", { a: 1 });
    spy.mockRestore();
  });
});
