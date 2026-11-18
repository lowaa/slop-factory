import { useCallback } from "react";
import { getRouteApi } from "@slop-factory/router";
import RemoteControlPage, { type RemoteControlSelection } from "./remote_control_page";

const remoteControlRouteApi = getRouteApi("/signed-in/remote-control");

// RemoteControlRoute keeps the open session in the address, so a reload or a
// link comes back to it. Opening another replaces nothing: Back steps through
// the sessions visited, except where the page picked the session itself.
export default function RemoteControlRoute() {
  const { conductor, session, draft, new: isNew, view } = remoteControlRouteApi.useSearch();
  const navigate = remoteControlRouteApi.useNavigate();
  const onSelect = useCallback(
    (next: RemoteControlSelection) =>
      void navigate({
        search: {
          conductor: next.conductorId,
          session: next.sessionId,
          draft: next.draft,
          new: next.isNew ? (1 as const) : undefined,
          view: next.view,
        },
        replace: next.replace,
      }),
    [navigate],
  );
  return (
    <RemoteControlPage
      conductorId={conductor}
      sessionId={session}
      draft={draft}
      isNew={isNew === 1}
      view={view}
      onSelect={onSelect}
    />
  );
}
