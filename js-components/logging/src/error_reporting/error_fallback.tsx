import { Button } from "@slop-factory/ui";
import "./error_fallback.scss";

interface ErrorFallbackProps {
  // Where "Go to start" leads. Left out where the router is not known to
  // work: the top-level fallback has to assume it doesn't.
  homeHref?: string;
}

// Deliberately says nothing of the error: what went wrong is for the log,
// and an error's text can hold what the user typed.
export function ErrorFallback({ homeHref }: ErrorFallbackProps) {
  return (
    <div className="error-fallback" role="alert">
      <h1 className="error-fallback__title">Something went wrong</h1>
      <p className="error-fallback__text">
        The page hit an unexpected problem. It has been reported. Reloading usually fixes it.
      </p>
      <div className="error-fallback__actions">
        <Button variant="primary" onClick={() => window.location.reload()}>
          Reload
        </Button>
        {/* A document load, not a router link: the router is what may have failed. */}
        {homeHref && <a href={homeHref}>Go to start</a>}
      </div>
    </div>
  );
}
