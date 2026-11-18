import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { apiClient, ApiError, type UserSetting } from "@/api";
import { useBoolUserSetting } from "./use_user_setting";
import { UserSettingsProvider } from "./user_settings_provider";

const NAME = "some.flag";

function saved(name: string, value: boolean | null): UserSetting {
  return { name, type: "bool", str_val: null, int_val: null, float_val: null, bool_val: value, is_default: value === null, updated_at: null };
}

function deferred<T>() {
  let resolve: (v: T) => void = () => {};
  let reject: (e: unknown) => void = () => {};
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const wrapper = ({ children }: { children: ReactNode }) => <UserSettingsProvider>{children}</UserSettingsProvider>;

describe("useBoolUserSetting", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(apiClient, "setMyBoolSetting").mockResolvedValue(saved(NAME, true));
  });

  it("shows the fallback, then the saved value once it arrives", async () => {
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([saved("other", false), saved(NAME, false)]);
    const { result } = renderHook(() => useBoolUserSetting(NAME, true), { wrapper });

    expect(result.current.value).toBe(true);
    await waitFor(() => expect(result.current.value).toBe(false));
  });

  it("keeps the fallback when the read fails", async () => {
    vi.spyOn(apiClient, "listMySettings").mockRejectedValue(new ApiError(500, "boom"));
    const { result } = renderHook(() => useBoolUserSetting(NAME, true), { wrapper });

    await waitFor(() => expect(apiClient.listMySettings).toHaveBeenCalled());
    expect(result.current.value).toBe(true);
  });

  it("keeps the fallback when the setting was never saved, whatever else was", async () => {
    const read = deferred<UserSetting[]>();
    vi.spyOn(apiClient, "listMySettings").mockReturnValue(read.promise);
    const { result } = renderHook(() => useBoolUserSetting(NAME, true), { wrapper });

    await act(async () => read.resolve([saved("other", false), saved(NAME, null)]));

    expect(result.current.value).toBe(true);
  });

  it("is not overwritten by a read that lands after the person's own change", async () => {
    const read = deferred<UserSetting[]>();
    vi.spyOn(apiClient, "listMySettings").mockReturnValue(read.promise);
    const { result } = renderHook(() => useBoolUserSetting(NAME, true), { wrapper });

    act(() => result.current.change(false));
    await act(async () => read.resolve([saved(NAME, true)]));

    expect(result.current.value).toBe(false);
    expect(apiClient.setMyBoolSetting).toHaveBeenCalledWith(NAME, false);
  });

  it("rolls a failed save back and says so", async () => {
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([]);
    vi.spyOn(apiClient, "setMyBoolSetting").mockRejectedValue(new ApiError(500, "boom"));
    const { result } = renderHook(() => useBoolUserSetting(NAME, true), { wrapper });

    act(() => result.current.change(false));

    await waitFor(() => expect(result.current.error).toBe("Could not save this preference."));
    expect(result.current.value).toBe(true);
  });

  it("rolls a failed save back to the saved value, not the fallback", async () => {
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([saved(NAME, false)]);
    vi.spyOn(apiClient, "setMyBoolSetting").mockRejectedValue(new ApiError(500, "boom"));
    const { result } = renderHook(() => useBoolUserSetting(NAME, true), { wrapper });
    await waitFor(() => expect(result.current.value).toBe(false));

    act(() => result.current.change(true));

    await waitFor(() => expect(result.current.error).not.toBeNull());
    expect(result.current.value).toBe(false);
  });

  it("rolls back to a value the read found after the change was made", async () => {
    const read = deferred<UserSetting[]>();
    vi.spyOn(apiClient, "listMySettings").mockReturnValue(read.promise);
    vi.spyOn(apiClient, "setMyBoolSetting").mockRejectedValue(new ApiError(500, "boom"));
    const { result } = renderHook(() => useBoolUserSetting(NAME, true), { wrapper });

    act(() => result.current.change(false));
    await waitFor(() => expect(result.current.error).not.toBeNull());
    await act(async () => read.resolve([saved(NAME, false)]));

    expect(result.current.value).toBe(false);
  });

  it("does not roll back an earlier save that fails after a later one was made", async () => {
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([saved(NAME, false)]);
    const first = deferred<UserSetting>();
    vi.spyOn(apiClient, "setMyBoolSetting").mockReturnValueOnce(first.promise).mockResolvedValue(saved(NAME, false));
    const { result } = renderHook(() => useBoolUserSetting(NAME, true), { wrapper });
    await waitFor(() => expect(result.current.value).toBe(false));

    act(() => result.current.change(true));
    act(() => result.current.change(false));
    await act(async () => first.reject(new ApiError(500, "late")));

    expect(result.current.value).toBe(false);
    expect(result.current.error).toBeNull();
  });

  it("reads once for every setting under one provider", async () => {
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([saved("a", true), saved("b", false)]);
    const { result } = renderHook(() => [useBoolUserSetting("a", false), useBoolUserSetting("b", true)] as const, { wrapper });

    await waitFor(() => expect(result.current[0].value).toBe(true));
    expect(result.current[1].value).toBe(false);
    expect(apiClient.listMySettings).toHaveBeenCalledTimes(1);
  });

  it("needs a provider", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    expect(() => renderHook(() => useBoolUserSetting(NAME, true))).toThrow(/UserSettingsProvider/);
  });
});
