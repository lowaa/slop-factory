import { Link } from "@slop-factory/router";
import { apiClient } from "@/api";
import { usePolledResource } from "@/lib/use_polled_resource";

// The merge queue batches only when exactly one manifest repo has apps and
// "Batch merges within an app" on (server/cmd/api/landing_batch.go); the
// batch size setting alone does nothing, which is easy to miss because the
// switch lives in the workspace manifest.
export default function BatchingStatus({ batchSize }: { batchSize: number }) {
  const workspace = usePolledResource(() => apiClient.getWorkspace(), {
    fallbackError: "Failed to load the workspace manifest.",
  });
  if (workspace.data === undefined) return null;
  const manifestLink = <Link to="/workspace">workspace manifest</Link>;

  if (batchSize <= 1) {
    return <p className="tenant-settings__meta">Batch size 1: every change lands alone.</p>;
  }
  const batching = (workspace.data.manifest?.repos ?? []).filter(
    (r) => r.batch_within_apps && (r.independent_apps ?? []).length > 0,
  );
  if (batching.length === 1) {
    const apps = batching[0].independent_apps ?? [];
    return (
      <p className="tenant-settings__meta">
        Batch size {batchSize}: <strong>active</strong> for {apps.length} {apps.length === 1 ? "app" : "apps"}. Changes touching files outside them land
        alone. Set in the {manifestLink}.
      </p>
    );
  }
  return (
    <p className="tenant-settings__meta">
      Batch size {batchSize}: <strong>not active</strong>.{" "}
      {batching.length > 1
        ? "Batching is on in more than one repo, so nothing batches; turn it on in one repo only."
        : "Declare apps and turn on 'Batch merges within an app'"}{" "}
      in the {manifestLink}.
    </p>
  );
}
