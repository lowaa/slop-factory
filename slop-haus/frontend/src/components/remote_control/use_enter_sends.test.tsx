import { describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryStore } from "@slop-factory/storage";
import type { UserSetting } from "@/api";
import { BrowserStorage } from "@/lib/browser_storage";
import { SettingsContext } from "@/lib/use_user_setting";
import { stubTouchDevice } from "@/test/layout";
import { useEnterSends } from "./use_enter_sends";

const KEY = "remote_control.enter_sends";

// The retired account setting is still listed to a tab that has it; it must
// not move this browser's default.
function account(value: boolean): UserSetting[] {
  return [
    {
      name: "remote_control.enter_sends",
      type: "bool",
      str_val: null,
      int_val: null,
      float_val: null,
      bool_val: value,
      is_default: false,
      updated_at: null,
    },
  ];
}

function setup(saved: UserSetting[] | null = null) {
  const local = new MemoryStore();
  const storage = new BrowserStorage({ local_storage: local, session_storage: new MemoryStore() });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <SettingsContext.Provider value={{ provided: true, saved }}>{children}</SettingsContext.Provider>
  );
  return { local, hook: () => renderHook(() => useEnterSends(storage), { wrapper }) };
}

describe("useEnterSends", () => {
  it("defaults on, and writes nothing until the person chooses", () => {
    const { local, hook } = setup();
    const { result } = hook();
    expect(result.current[0]).toBe(true);
    expect(local.get(KEY)).toBeNull();

    act(() => result.current[1](false));

    expect(local.get(KEY)).toBe("off");
    expect(result.current[0]).toBe(false);
  });

  it("defaults off where the primary input is touch", () => {
    stubTouchDevice();
    expect(setup().hook().result.current[0]).toBe(false);
  });

  it("follows the primary input changing while no choice is made", () => {
    const { setTouch } = stubTouchDevice(false);
    const { result } = setup().hook();
    expect(result.current[0]).toBe(true);

    act(() => setTouch(true));
    expect(result.current[0]).toBe(false);

    act(() => setTouch(false));
    expect(result.current[0]).toBe(true);
  });

  it("lets a stored choice win over the default", () => {
    stubTouchDevice();
    const { local, hook } = setup();
    local.set(KEY, "on");
    expect(hook().result.current[0]).toBe(true);
  });

  it("ignores a stored account setting of off", () => {
    const { local, hook } = setup(account(false));
    expect(hook().result.current[0]).toBe(true);
    expect(local.get(KEY)).toBeNull();
  });
});
