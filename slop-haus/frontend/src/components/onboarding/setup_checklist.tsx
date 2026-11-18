import { Link } from "@slop-factory/router";
import { useOnboardingStatus } from "./use_onboarding_status";
import "./setup_checklist.scss";

interface SetupChecklistProps {
  // Changes whenever the board's own live refresh fires.
  refreshToken: number;
}

// Steers a new tenant to the setup its workers need before they can do code
// work. Gone once every step is done, so a set-up board is exactly what it was.
export default function SetupChecklist({ refreshToken }: SetupChecklistProps) {
  const status = useOnboardingStatus(refreshToken);
  if (!status || status.steps.every((step) => step.done)) return null;
  return (
    <section className="setup-checklist" role="status" aria-label="Setup">
      <h2 className="setup-checklist__heading">Finish setting up</h2>
      {!status.code_work_ready && (
        <p className="setup-checklist__text">No worker can do code work until this is done: they would have nothing to clone.</p>
      )}
      <ul className="setup-checklist__steps">
        {status.steps.map((step) => (
          <li key={step.key} className={`setup-checklist__step${step.done ? " setup-checklist__step--done" : ""}`}>
            <span aria-hidden="true">{step.done ? "✓" : "○"}</span>{" "}
            {step.done ? (
              <span>{step.title} (done)</span>
            ) : (
              <>
                <Link to={step.path}>{step.title}</Link>: {step.detail}
              </>
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}
