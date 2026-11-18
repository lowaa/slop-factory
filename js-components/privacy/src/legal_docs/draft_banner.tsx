import "./draft_banner.scss";

// Shown for every document whose text has not been legally reviewed. It is
// the page's, not the body's, so reviewed text can't carry it forward by
// accident and placeholder text can't leave it out.
export default function DraftBanner() {
  return (
    <div className="draft-banner" role="note">
      <strong>DRAFT – not legally reviewed</strong>
      <span> This text is a placeholder and is not yet in force.</span>
    </div>
  );
}
