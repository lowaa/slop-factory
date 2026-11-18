import { invalidate, type ResourceKey } from "@/lib/resource";
import { useResourceClient } from "@/lib/resource/client";
import { useLiveRefresh } from "./use_live_refresh";

// useLiveInvalidate marks the keys stale whenever the board changes, which
// refetches them where a view is showing them. Throttled like any live refresh.
export function useLiveInvalidate(keys: ResourceKey[]): void {
  const client = useResourceClient();
  const hash = JSON.stringify(keys);
  useLiveRefresh(() => {
    for (const key of JSON.parse(hash) as ResourceKey[]) void invalidate(key, client);
  });
}
