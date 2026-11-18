import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { apiClient, type UserSetting } from "@/api";
import { RENDER_MARKDOWN, useRenderMarkdown } from "./markdown_setting";
import { UserSettingsProvider } from "./user_settings_provider";

function saved(value: boolean | null): UserSetting {
  return { name: RENDER_MARKDOWN, type: "bool", str_val: null, int_val: null, float_val: null, bool_val: value, is_default: value === null, updated_at: null };
}

const wrapper = ({ children }: { children: ReactNode }) => <UserSettingsProvider>{children}</UserSettingsProvider>;

describe("useRenderMarkdown", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(apiClient, "setMyBoolSetting").mockResolvedValue(saved(true));
  });

  it("is on by default, before and without a saved value", async () => {
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([saved(null)]);
    const { result } = renderHook(() => useRenderMarkdown(), { wrapper });
    expect(result.current.value).toBe(true);
    await waitFor(() => expect(apiClient.listMySettings).toHaveBeenCalled());
    expect(result.current.value).toBe(true);
  });

  it("shows the saved value when the person turned it off", async () => {
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([saved(false)]);
    const { result } = renderHook(() => useRenderMarkdown(), { wrapper });
    await waitFor(() => expect(result.current.value).toBe(false));
  });

  it("saves a change", async () => {
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([]);
    const { result } = renderHook(() => useRenderMarkdown(), { wrapper });
    act(() => result.current.change(false));
    expect(apiClient.setMyBoolSetting).toHaveBeenCalledWith(RENDER_MARKDOWN, false);
    expect(result.current.value).toBe(false);
  });

  it("flips for the page without saving, and leaves the saved value alone", async () => {
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([saved(true)]);
    const { result } = renderHook(() => useRenderMarkdown(), { wrapper });
    await waitFor(() => expect(apiClient.listMySettings).toHaveBeenCalled());

    act(() => result.current.toggleForPage());
    expect(result.current.value).toBe(false);
    expect(result.current.saved).toBe(true);
    act(() => result.current.toggleForPage());
    expect(result.current.value).toBe(true);
    expect(apiClient.setMyBoolSetting).not.toHaveBeenCalled();
  });

  it("a saved change replaces the page's override", async () => {
    vi.spyOn(apiClient, "listMySettings").mockResolvedValue([saved(true)]);
    const { result } = renderHook(() => useRenderMarkdown(), { wrapper });
    act(() => result.current.toggleForPage());
    act(() => result.current.change(true));
    expect(result.current.value).toBe(true);
    expect(apiClient.setMyBoolSetting).toHaveBeenCalledWith(RENDER_MARKDOWN, true);
  });
});
