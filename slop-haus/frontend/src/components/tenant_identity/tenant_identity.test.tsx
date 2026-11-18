import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { apiClient, ApiError } from "@/api";
import { resetTenantFaviconCache } from "@/lib/tenant_favicon";
import { defaultTenantColour } from "@/lib/tenant_colour";
import { defaultResourceClient, invalidate, resourceKeys } from "@/lib/resource";
import { makeTenant } from "@/test/fixtures";
import TenantIdentity from "./tenant_identity";

const stripe = (container: HTMLElement) => container.querySelector<HTMLElement>(".tenant-stripe");

const ADA = makeTenant({ id: "t1", name: "Ada's workspace", current: true });
const LAB = makeTenant({ id: "t2", name: "Lab", current: false });

afterEach(() => vi.restoreAllMocks());

describe("TenantIdentity", () => {
  it("shows the stripe, the badge and the name to a person in more than one tenant", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([ADA, LAB]);
    const { container } = render(<TenantIdentity />);

    expect(await screen.findByText("Ada's workspace")).toBeInTheDocument();
    expect(screen.getByText("AW")).toBeInTheDocument();
    expect(stripe(container)).toHaveAttribute("data-colour", defaultTenantColour("t1"));
  });

  // The desktop header leaves the name to the tenant switcher beside it.
  it("shows the stripe without the badge when asked to", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([ADA, LAB]);
    const { container } = render(<TenantIdentity badge={false} />);

    await vi.waitFor(() => expect(stripe(container)).toHaveAttribute("data-colour", defaultTenantColour("t1")));
    expect(screen.queryByText("Ada's workspace")).toBeNull();
    expect(screen.queryByText("AW")).toBeNull();
  });

  it("uses the owner's colour over the default", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([{ ...ADA, colour: "violet" }, LAB]);
    const { container } = render(<TenantIdentity />);

    await screen.findByText("Ada's workspace");
    expect(stripe(container)).toHaveAttribute("data-colour", "violet");
  });

  it("shows nothing to a person in one tenant whose owner chose no colour", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([ADA]);
    const { container } = render(<TenantIdentity />);

    await vi.waitFor(() => expect(apiClient.listTenants).toHaveBeenCalled());
    await act(async () => {});
    expect(container).toBeEmptyDOMElement();
  });

  it("shows only the stripe to a person in one tenant whose owner chose a colour", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([{ ...ADA, colour: "teal" }]);
    const { container } = render(<TenantIdentity />);

    await vi.waitFor(() => expect(stripe(container)).toHaveAttribute("data-colour", "teal"));
    expect(screen.queryByText("Ada's workspace")).toBeNull();
    expect(screen.queryByText("AW")).toBeNull();
  });

  it("shows nothing when the list cannot be read", async () => {
    vi.spyOn(apiClient, "listTenants").mockRejectedValue(new ApiError(500, "boom"));
    const { container } = render(<TenantIdentity />);

    await vi.waitFor(() => expect(apiClient.listTenants).toHaveBeenCalled());
    await act(async () => {});
    expect(container).toBeEmptyDOMElement();
  });

  it("follows a colour change made on the settings page without a reload", async () => {
    const list = vi.spyOn(apiClient, "listTenants").mockResolvedValue([ADA, LAB]);
    const { container } = render(<TenantIdentity />);
    await screen.findByText("Ada's workspace");

    list.mockResolvedValue([{ ...ADA, colour: "pink" }, LAB]);
    await act(async () => invalidate(resourceKeys.tenants()));
    expect(stripe(container)).toHaveAttribute("data-colour", "pink");
  });

  it("names the tenant on the badge alone when compact", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([ADA, LAB]);
    render(<TenantIdentity compact />);

    expect(await screen.findByRole("img", { name: "Ada's workspace" })).toBeInTheDocument();
    expect(screen.queryByText("Ada's workspace")).toBeNull();
  });

  it("prefixes the tab title for a person in more than one tenant, and drops it on unmount", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([ADA, LAB]);
    const { unmount } = render(<TenantIdentity />);

    await vi.waitFor(() => expect(document.title).toBe("Ada's workspace · slop-haus"));
    unmount();
    expect(document.title).toBe("slop-haus");
  });

  it("prefixes the tab title for a single tenant only when its owner chose a colour", async () => {
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([ADA]);
    const plain = render(<TenantIdentity />);
    await vi.waitFor(() => expect(apiClient.listTenants).toHaveBeenCalled());
    await act(async () => {});
    expect(document.title).toBe("slop-haus");
    plain.unmount();
    // A second visit would otherwise be answered from the cache.
    defaultResourceClient.clear();

    vi.spyOn(apiClient, "listTenants").mockResolvedValue([{ ...ADA, colour: "teal" }]);
    render(<TenantIdentity />);
    await vi.waitFor(() => expect(document.title).toBe("Ada's workspace · slop-haus"));
  });

  it("tints the tab icon with the tenant's colour, and removes it on unmount", async () => {
    resetTenantFaviconCache();
    document.documentElement.style.setProperty("--tenant-violet-bg", "#8844ff");
    const ctx = { beginPath() {}, arc() {}, fill() {}, stroke() {}, canvas: { toDataURL: () => "data:image/png;base64,violet" } };
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(ctx as unknown as CanvasRenderingContext2D);
    vi.spyOn(apiClient, "listTenants").mockResolvedValue([{ ...ADA, colour: "violet" }, LAB]);
    const { unmount } = render(<TenantIdentity />);

    await vi.waitFor(() => expect(document.head.querySelector("link[rel=icon]")).toHaveAttribute("href", "data:image/png;base64,violet"));
    unmount();
    expect(document.head.querySelector("link[rel=icon]")).toBeNull();
    document.documentElement.style.removeProperty("--tenant-violet-bg");
    resetTenantFaviconCache();
  });
});
