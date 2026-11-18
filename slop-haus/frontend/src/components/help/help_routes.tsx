import { Link, getRouteApi } from "@slop-factory/router";
import GuideView from "./guide_view";
import { GUIDE_PAGES, findGuidePage } from "./guide_pages";
import "./help.scss";

// By id rather than importing the route from routes.tsx, which imports this.
const helpPageRouteApi = getRouteApi("/signed-in/help/$slug");

export function HelpIndex() {
  return (
    <div className="help-index">
      <h2>Help</h2>
      <ul>
        {GUIDE_PAGES.map((page) => (
          <li key={page.slug}>
            <Link to="/help/$slug" params={{ slug: page.slug }}>
              {page.title}
            </Link>
          </li>
        ))}
      </ul>
    </div>
  );
}

export function HelpPage({ slug }: { slug: string }) {
  const page = findGuidePage(slug);
  return (
    <div className="help-page">
      <Link to="/help" className="help-page-back">
        ← All help
      </Link>
      {page ? <GuideView page={page} /> : <p>There is no help page called &ldquo;{slug}&rdquo;.</p>}
    </div>
  );
}

export function HelpPageRoute() {
  const { slug } = helpPageRouteApi.useParams();
  return <HelpPage slug={slug} />;
}
