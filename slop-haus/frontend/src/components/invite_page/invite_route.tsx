import { useNavigateToPath } from "@slop-factory/router";
import InvitePage from "./invite_page";

export default function InviteRoute() {
  const navigate = useNavigateToPath();
  return <InvitePage onNavigate={navigate} />;
}
