import { Link } from "@slop-factory/router";
import type { Tab } from "@/tabs";
import { rememberedFilterSearch } from "@/components/ticket_filter";
import "./back_link.scss";

// BackLink goes to a tab, named after it: a ticket is reachable from more
// than one tab, and "Back to board" from the blocking graph would be a lie
// that also loses your place in it. A link to the tab rather than history
// back, so it goes to the same place however the ticket was reached (a
// pasted link has no history to go back to); the browser's own Back is
// there for history.
//
// Back to the board keeps the filters it last had, in the link itself
// rather than applied by the board when its address has none: the address
// of a filtered board is meant to be a link that shows it filtered.
export default function BackLink({ tab }: { tab: Tab }) {
  // A tab's filter is remembered under the tab's name.
  const search = rememberedFilterSearch(tab.name);
  return (
    <Link to={tab.path} search={search} className="back-link">
      {tab.backLabel}
    </Link>
  );
}
