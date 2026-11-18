import "./loading_spinner.scss";

// Shown while static_config or other app-blocking data is in flight. Kept
// dependency-free (no animation library) since it's just a CSS spin.
export default function LoadingSpinner() {
  return (
    <div className="loading-spinner" role="status" aria-live="polite">
      <div className="loading-spinner__ring" />
      <span className="loading-spinner__label">Loading…</span>
    </div>
  );
}
