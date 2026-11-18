import { vi, type MockInstance } from "vitest";
import { apiClient } from "@/api";

type Api = typeof apiClient;
type Methods = {
  [K in keyof Api]: Api[K] extends (...args: never[]) => Promise<unknown> ? K : never;
}[keyof Api];

// MockedReplies maps api methods to the value each should resolve with.
export type MockedReplies = { [K in Methods]?: Awaited<ReturnType<Api[K]>> };

// mockApi stubs each named apiClient method to resolve with the given value,
// and returns the spies by method name for assertions on their calls. A test
// that needs a rejection or a deferred reply spies on that method itself.
export function mockApi<R extends MockedReplies>(replies: R) {
  const spies: Record<string, unknown> = {};
  for (const [name, value] of Object.entries(replies)) {
    const method = name as Methods;
    const spy = vi.spyOn(apiClient, method) as unknown as MockInstance;
    spies[name] = spy.mockResolvedValue(value);
  }
  return spies as { [K in keyof R]: MockInstance };
}
