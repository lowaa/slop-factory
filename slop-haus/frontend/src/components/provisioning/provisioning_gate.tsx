import type { ReactNode } from "react";
import { apiClient } from "@/api";
import { usePolledResource } from "@/lib/use_polled_resource";

export const PROVISION_FROM_BOARD = "provision_from_board";

// Renders its children only while the tenant's provision_from_board setting
// is on, the fallback otherwise. Anything short of a clear "on" (loading, a
// failed read, a person who may not read it) shows the fallback, so a tenant
// with it on sees the fallback until the setting arrives.
interface Props {
  children: ReactNode;
  fallback?: ReactNode;
}

export default function ProvisioningGate({ children, fallback = null }: Props) {
  const flag = usePolledResource(() => apiClient.getSetting(PROVISION_FROM_BOARD));
  return <>{flag.data?.bool_val === true ? children : fallback}</>;
}
