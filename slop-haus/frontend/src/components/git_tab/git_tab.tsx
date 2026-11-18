import { GitHubAppPanel } from "@/components/github_app_panel";
import { GitHubWebhooksPanel } from "@/components/github_webhooks_panel";

// GitTab is the Git route's component: the GitHub App slop-haus acts as, and
// the webhooks GitHub calls back on.
export default function GitTab() {
  return (
    <>
      <GitHubAppPanel />
      <GitHubWebhooksPanel />
    </>
  );
}
