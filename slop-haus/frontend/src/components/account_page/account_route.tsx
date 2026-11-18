import { useAuth } from "@/context/auth";
import AccountPage from "./account_page";

export default function AccountRoute() {
  const { profile } = useAuth();
  // Only ever rendered inside AppShell, which renders nothing without a
  // profile; this just narrows the type.
  return profile ? <AccountPage profile={profile} /> : null;
}
