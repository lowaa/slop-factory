import { describe, expect, it, vi } from "vitest";
import { bindPersonalStateUser, forgetPersonalState, registerPersonalState } from "./personal_state";

describe("personal state registry", () => {
  it("forgets and binds every registered entry", () => {
    const a = { forget: vi.fn(), bindUser: vi.fn() };
    const b = { forget: vi.fn() };
    registerPersonalState("test/a", a);
    registerPersonalState("test/b", b);

    forgetPersonalState();
    bindPersonalStateUser("u-1");

    expect(a.forget).toHaveBeenCalledTimes(1);
    expect(b.forget).toHaveBeenCalledTimes(1);
    expect(a.bindUser).toHaveBeenCalledWith("u-1");
  });

  it("replaces an entry registered again under the same name", () => {
    const first = { forget: vi.fn() };
    const second = { forget: vi.fn() };
    registerPersonalState("test/dup", first);
    registerPersonalState("test/dup", second);

    forgetPersonalState();

    expect(first.forget).not.toHaveBeenCalled();
    expect(second.forget).toHaveBeenCalledTimes(1);
  });
});
