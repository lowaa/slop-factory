import { getRouteApi } from "@slop-factory/router";
import GitHubAppCallback from "./github_app_callback";

const githubAppCreatedApi = getRouteApi("/signed-in/workspace/github-app/created");
const githubAppInstalledApi = getRouteApi("/signed-in/workspace/github-app/installed");

export function GitHubAppCreatedRoute() {
  return <GitHubAppCallback step="created" search={githubAppCreatedApi.useSearch()} />;
}

export function GitHubAppInstalledRoute() {
  return <GitHubAppCallback step="installed" search={githubAppInstalledApi.useSearch()} />;
}
