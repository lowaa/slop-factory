import { useId, useState } from "react";
import { apiClient, type LandingCapabilities } from "@/api";
import { usePolledResource } from "@/lib/use_polled_resource";
import { errorMessage } from "@/lib/use_async_action";

export const MERGE_METHOD = "merge_method";

type Method = LandingCapabilities["merge_method"];

const CHOICES: { value: Method; label: string; description: string }[] = [
  {
    value: "worker",
    label: "The worker merges",
    description:
      "The agent that finished the change takes the integration lock, merges main, waits for CI and merges. Tickets already in the merge queue are handed back to their authors, to land the old way.",
  },
  {
    value: "merge_queue",
    label: "The server merges from a queue",
    description:
      "When a worker finishes, the server merges main, waits for CI and merges; workers no longer hold the integration lock.",
  },
];

// Why merge_queue cannot be chosen, or null when it can.
export function mergeQueueUnavailableReason(
  c: LandingCapabilities,
): string | null {
  if (c.merge_queue_available) return null;
  if (!c.merge_queue_enabled)
    return "The merge queue is switched off for this server.";
  return "Connect a code host first: the server needs its credential to merge for you.";
}

interface MergeMethodRowProps {
  // Resolves once the new value is stored; a rejection is shown against the row.
  onWrite: (next: Method) => Promise<void>;
  children?: React.ReactNode;
}

// The owner's setting; the server refuses anyone else's write, and that
// refusal is shown against the row.
export default function MergeMethodRow({
  onWrite,
  children,
}: MergeMethodRowProps) {
  const caps = usePolledResource(() => apiClient.getLandingCapabilities());
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const name = useId();

  if (!caps.data) {
    return (
      <div>
        <span className="tenant-settings__name">
          How finished changes are merged
        </span>
        {caps.status === "error" && (
          <p role="alert" className="tenant-settings__error">
            {caps.error}
          </p>
        )}
        {children}
      </div>
    );
  }
  const current = caps.data.merge_method;
  const reason = mergeQueueUnavailableReason(caps.data);

  async function choose(next: Method) {
    if (saving || next === current) return;
    setSaving(true);
    setError(null);
    try {
      await onWrite(next);
      await caps.reload();
    } catch (err: unknown) {
      setError(
        errorMessage(err, "Failed to change how finished changes are merged."),
      );
    } finally {
      setSaving(false);
    }
  }

  return (
    <fieldset className="tenant-settings__choices" disabled={saving}>
      <legend className="tenant-settings__name">
        How finished changes are merged
      </legend>
      {CHOICES.map((choice) => {
        // A tenant already on merge_queue can always leave it; only choosing it is gated.
        const blocked =
          choice.value === "merge_queue" &&
          reason !== null &&
          current !== "merge_queue";
        const describedBy = `${name}-${choice.value}-desc ${name}-${choice.value}-note`;
        return (
          <div
            key={choice.value}
            className="tenant-settings__choice"
            data-testid="merge-method-choice"
          >
            <label className="tenant-settings__choice-label">
              <input
                type="radio"
                data-native-look="a two-way choice: the native radio group is the clearest control"
                name={name}
                checked={current === choice.value}
                disabled={blocked}
                aria-describedby={
                  blocked ? describedBy : `${name}-${choice.value}-desc`
                }
                onChange={() => void choose(choice.value)}
              />
              {choice.label}
            </label>
            <p
              id={`${name}-${choice.value}-desc`}
              className="tenant-settings__muted tenant-settings__choice-detail"
            >
              {choice.description}
            </p>
            {blocked && (
              <p
                id={`${name}-${choice.value}-note`}
                role="note"
                className="tenant-settings__muted tenant-settings__choice-detail"
              >
                {reason}
              </p>
            )}
          </div>
        );
      })}
      {children}
      {error && (
        <p role="alert" className="tenant-settings__error">
          {error}
        </p>
      )}
    </fieldset>
  );
}
