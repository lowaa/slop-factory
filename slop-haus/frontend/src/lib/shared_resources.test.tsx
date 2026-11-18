import { describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { apiClient } from "@/api";
import { makeAgent, makeTenant } from "@/test/fixtures";
import { invalidate, resourceKeys } from "./resource";
import { readAgents, useAgents, useTenants, useTenantsNow } from "./shared_resources";

function TenantNames({ id }: { id: string }) {
  const { data } = useTenants();
  return <p data-testid={id}>{(data ?? []).map((t) => t.name).join(",")}</p>;
}

describe("shared resources", () => {
  it("two consumers of the tenants make one request, and an invalidation refreshes both", async () => {
    const list = vi.spyOn(apiClient, "listTenants").mockResolvedValueOnce([makeTenant({ name: "Old" })]).mockResolvedValue([makeTenant({ name: "New" })]);
    render(
      <>
        <TenantNames id="a" />
        <TenantNames id="b" />
      </>,
    );

    expect(await screen.findAllByText("Old")).toHaveLength(2);
    expect(list).toHaveBeenCalledTimes(1);

    await act(async () => invalidate(resourceKeys.tenants()));
    expect(await screen.findAllByText("New")).toHaveLength(2);
    expect(list).toHaveBeenCalledTimes(2);
  });

  it("keeps the archived and default agent lists apart, and invalidating agents refreshes both", async () => {
    const list = vi.spyOn(apiClient, "listAgents").mockImplementation(async (o) => (o?.includeArchived ? [makeAgent({ name: "all" })] : [makeAgent({ name: "live" })]));
    function Both() {
      const all = useAgents({ archived: true }).data;
      const live = useAgents({ archived: false }).data;
      return <p>{`${all?.[0]?.name}/${live?.[0]?.name}`}</p>;
    }
    render(<Both />);
    expect(await screen.findByText("all/live")).toBeInTheDocument();

    await act(async () => invalidate(["agents"]));
    expect(list).toHaveBeenCalledTimes(4);
  });

  it("readAgents shares the request in flight and fills the cache for views", async () => {
    const list = vi.spyOn(apiClient, "listAgents").mockResolvedValue([makeAgent({ name: "live" })]);
    const [a, b] = await Promise.all([readAgents({ archived: false }), readAgents({ archived: false })]);
    expect(a).toEqual(b);
    expect(list).toHaveBeenCalledTimes(1);
    function View() {
      return <p>{useAgents({ archived: false }).data?.[0]?.name}</p>;
    }
    render(<View />);
    expect(await screen.findByText("live")).toBeInTheDocument();
    expect(list).toHaveBeenCalledTimes(1);
  });

  it("useTenantsNow fetches afresh although the tenants are cached", async () => {
    const list = vi.spyOn(apiClient, "listTenants").mockResolvedValueOnce([makeTenant({ name: "Old" })]).mockResolvedValue([makeTenant({ name: "New" })]);
    const first = render(<TenantNames id="a" />);
    await screen.findByText("Old");
    first.unmount();
    function Now() {
      return <p>{useTenantsNow().data?.[0]?.name}</p>;
    }
    render(<Now />);
    expect(await screen.findByText("New")).toBeInTheDocument();
    expect(list).toHaveBeenCalledTimes(2);
  });
});
