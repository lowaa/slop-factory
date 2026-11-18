// Makes every method of an API client fail a story unless the story stubbed
// it. Replacing the methods in place (as vi.spyOn does in a component test)
// means components need no injection point.
//
// A component usually catches a failed call and shows an error state, which
// would let the story pass with the call unstubbed; so a call is also
// recorded, and assertNoUnstubbedCalls (the global afterEach) fails the story
// even when the component swallowed the error.

export interface ApiGuardOptions {
  // How the client is named in messages, e.g. "apiClient".
  clientName?: string;
  // What the app calls its stub helper, for the failure message.
  stubName?: string;
}

export interface ApiGuard<C extends object> {
  // The project-wide beforeEach: every function-valued property, own or inherited, of the
  // client becomes one that records its name and throws. Returns the cleanup
  // that puts the real ones back.
  guard(): () => void;
  // Replaces client methods for the running story.
  stub(stubs: Partial<C>): void;
  // The global afterEach; clears what it reports.
  assertNoUnstubbedCalls(): void;
}

// Function-valued properties of the client, own or inherited from a class
// (stopping short of Object.prototype).
function methodNames(client: object): Set<string> {
  const names = new Set<string>();
  for (let obj: object | null = client; obj && obj !== Object.prototype; obj = Object.getPrototypeOf(obj)) {
    for (const name of Object.getOwnPropertyNames(obj)) {
      if (name === "constructor") continue;
      const descriptor = Object.getOwnPropertyDescriptor(obj, name);
      if (typeof descriptor?.value === "function") names.add(name);
    }
  }
  return names;
}

export function createApiGuard<C extends object>(client: C, options: ApiGuardOptions = {}): ApiGuard<C> {
  const clientName = options.clientName ?? "client";
  const stubName = options.stubName ?? "stub";
  let unstubbedCalls: string[] = [];
  const target = client as Record<string, unknown>;

  return {
    guard() {
      unstubbedCalls = [];
      const originals = new Map<string, PropertyDescriptor | undefined>();
      for (const name of methodNames(client)) {
        originals.set(name, Object.getOwnPropertyDescriptor(client, name));
        target[name] = async () => {
          unstubbedCalls.push(name);
          throw new Error(`${clientName}.${name} was called in a story without a stub`);
        };
      }
      return () => {
        for (const [name, descriptor] of originals) {
          // A method inherited from a prototype had no own property to put back.
          if (descriptor) Object.defineProperty(client, name, descriptor);
          else delete target[name];
        }
      };
    },

    stub(stubs) {
      Object.assign(client, stubs);
    },

    assertNoUnstubbedCalls() {
      const calls = unstubbedCalls;
      unstubbedCalls = [];
      if (calls.length) {
        const names = [...new Set(calls)].map((name) => `${clientName}.${name}`).join(", ");
        throw new Error(`${names} called without a stub: add ${stubName}({...}) in the story's beforeEach`);
      }
    },
  };
}
