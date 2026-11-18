import { useEffect, useRef, useState } from "react";
import { useNavigateToPath } from "@slop-factory/router";
import { Button } from "@slop-factory/ui";
import { apiClient, ApiError } from "@/api";
import StepUpPrompt from "@/shared/step_up_prompt/step_up_prompt";
import { redirectBrowserTo } from "@/lib/navigation";
import "./github_app_callback.scss";
import { errorMessage } from "@/lib/use_async_action";

// These stay under /workspace: the server writes them into the manifest it
// hands GitHub, and Apps created from it before the Git tab existed redirect
// there. Only where the browser lands afterwards moved.
const CREATED_PATH = "/workspace/github-app/created";
const INSTALLED_PATH = "/workspace/github-app/installed";

export interface CallbackSearch {
  code?: string;
  state?: string;
  installation_id?: string;
  setup_action?: string;
}

type Outcome =
  | { status: "working" }
  | { status: "failed"; message: string }
  | { status: "reauth" }
  | { status: "requested" };

// parseInstallationId is the id GitHub put in the address, or 0 for none or
// anything that is not a whole number: the server refuses an id GitHub does
// not know as this App's.
function parseInstallationId(raw: string | undefined): number {
  return raw !== undefined && /^[1-9][0-9]*$/.test(raw) ? Number(raw) : 0;
}

interface CallbackProps {
  step: "created" | "installed";
  search: CallbackSearch;
}

// GitHubAppCallback is where GitHub sends the browser back to, with a code
// (the App was created) or an installation id (it was installed). Both are
// only claims from the address bar: this page hands them to the server, which
// checks the state it issued, asks GitHub itself, and stores what it learns.
// Nothing here is shown a key.
export default function GitHubAppCallback({ step, search }: CallbackProps) {
  const navigate = useNavigateToPath();
  const [outcome, setOutcome] = useState<Outcome>({ status: "working" });
  // The server spends a state on first use, so an effect that runs twice
  // (Strict Mode) must not send it twice. A step-up retry sets it back.
  const started = useRef(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    if (started.current) return;
    started.current = true;
    void run();

    async function run() {
      try {
        if (step === "created") {
          const created = await apiClient.completeGitHubAppManifest(
            search.code ?? "",
            search.state ?? "",
          );
          // On to GitHub's install page: choosing the repositories is the next
          // click, so the owner is not brought back to the board in between.
          redirectBrowserTo(created.install_url);
          return;
        }
        const installed = await apiClient.completeGitHubAppInstall({
          installation_id: parseInstallationId(search.installation_id),
          setup_action: search.setup_action ?? "",
          state: search.state ?? "",
        });
        if (installed.requested) {
          setOutcome({ status: "requested" });
          return;
        }
        // Verified straight away so the panel ends at connected and verified;
        // if GitHub refuses, the panel's own Verify says why.
        await apiClient.verifyGitHubApp().catch(() => undefined);
        navigate("/git");
      } catch (err) {
        if (err instanceof ApiError && err.code === "reauth_required") {
          setOutcome({ status: "reauth" });
        } else {
          setOutcome({
            status: "failed",
            message:
              errorMessage(err, "That did not work. Go back to the Git tab and start again."),
          });
        }
      }
    }
  }, [step, search, navigate, attempt]);

  function retryAfterStepUp() {
    started.current = false;
    setOutcome({ status: "working" });
    setAttempt((n) => n + 1);
  }

  return (
    <div className="github-app-callback">
      <h2>GitHub App</h2>
      {outcome.status === "working" && (
        <p role="status">
          {step === "created"
            ? "Finishing creating the App…"
            : "Confirming the installation with GitHub…"}
        </p>
      )}
      {outcome.status === "reauth" && (
        <StepUpPrompt
          next={`${step === "created" ? CREATED_PATH : INSTALLED_PATH}${window.location.search}`}
          onConfirmed={retryAfterStepUp}
          onCancel={() => navigate("/git")}
        />
      )}
      {outcome.status === "requested" && (
        <>
          <p role="status">
            Waiting for an org owner to approve the install. GitHub has asked
            them; once they approve it, press Verify on the Git tab and
            the board picks the installation up.
          </p>
          <Button variant="primary" onClick={() => navigate("/git")}>
            Back to Git
          </Button>
        </>
      )}
      {outcome.status === "failed" && (
        <>
          <p role="alert" className="github-app-callback__error">
            {outcome.message}
          </p>
          <Button onClick={() => navigate("/git")}>
            Back to Git
          </Button>
        </>
      )}
    </div>
  );
}
