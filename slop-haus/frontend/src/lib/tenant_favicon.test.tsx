import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { resetTenantFaviconCache, tenantFaviconUrl, useTenantFavicon } from "./tenant_favicon";
import type { TenantColour } from "./tenant_colour";

function fakeCanvas(): { create: () => HTMLCanvasElement; fills: string[] } {
  const fills: string[] = [];
  const ctx = {
    beginPath() {},
    arc() {},
    fill() {},
    stroke() {},
    lineWidth: 0,
    strokeStyle: "",
    set fillStyle(v: string) {
      fills.push(v);
    },
    canvas: { toDataURL: () => `data:image/png;base64,${fills[fills.length - 1]}` },
  };
  return { create: () => ({ getContext: () => ctx }) as unknown as HTMLCanvasElement, fills };
}

beforeEach(() => {
  resetTenantFaviconCache();
  document.documentElement.style.setProperty("--tenant-teal-bg", "#00aaaa");
});
afterEach(() => {
  document.documentElement.style.removeProperty("--tenant-teal-bg");
  document.head.querySelectorAll("link[rel=icon]").forEach((l) => l.remove());
});

function Probe({ colour }: { colour: TenantColour | null }) {
  useTenantFavicon(colour);
  return null;
}

describe("tenantFaviconUrl", () => {
  it("draws the tenant's colour once and reuses it", () => {
    const canvas = fakeCanvas();
    const first = tenantFaviconUrl("teal", canvas.create);
    const second = tenantFaviconUrl("teal", canvas.create);
    expect(first).toBe("data:image/png;base64,#00aaaa");
    expect(second).toBe(first);
    expect(canvas.fills).toEqual(["#00aaaa"]);
  });

  it("draws each colour separately", () => {
    document.documentElement.style.setProperty("--tenant-red-bg", "#ff0000");
    const canvas = fakeCanvas();
    expect(tenantFaviconUrl("teal", canvas.create)).not.toBe(tenantFaviconUrl("red", canvas.create));
    document.documentElement.style.removeProperty("--tenant-red-bg");
  });

  it("is null where canvas is unavailable", () => {
    expect(tenantFaviconUrl("teal", () => ({ getContext: () => null }) as unknown as HTMLCanvasElement)).toBeNull();
  });

  it("is null for a colour with no token", () => {
    expect(tenantFaviconUrl("pink", fakeCanvas().create)).toBeNull();
  });
});

describe("useTenantFavicon", () => {
  it("sets the icon with the tenant colour and restores on unmount", () => {
    const canvas = fakeCanvas();
    vi.spyOn(document, "createElement").mockImplementation(((tag: string) =>
      tag === "canvas" ? canvas.create() : Document.prototype.createElement.call(document, tag)));
    const { unmount } = render(<Probe colour="teal" />);

    expect(document.head.querySelector("link[rel=icon]")).toHaveAttribute("href", "data:image/png;base64,#00aaaa");
    unmount();
    expect(document.head.querySelector("link[rel=icon]")).toBeNull();
    vi.restoreAllMocks();
  });

  it("adds no icon when drawing falls back", () => {
    render(<Probe colour="pink" />);
    expect(document.head.querySelector("link[rel=icon]")).toBeNull();
  });
});
