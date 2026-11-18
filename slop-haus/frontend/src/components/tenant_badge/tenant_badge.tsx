import { tenantInitials, type TenantColour } from "@/lib/tenant_colour";
import "./tenant_badge.scss";

// TenantBadge is a tenant's initials on its colour, with its name beside
// it unless compact (a phone's header has no room; the name is then the
// badge's label). Used wherever a tenant is shown by identity.
export default function TenantBadge({
  name,
  colour,
  compact = false,
}: {
  name: string;
  colour: TenantColour;
  compact?: boolean;
}) {
  return (
    <span
      className="tenant-badge"
      {...(compact ? { role: "img", "aria-label": name, title: name } : {})}
    >
      <span className="tenant-badge__initials" data-colour={colour} aria-hidden="true">
        {tenantInitials(name)}
      </span>
      {!compact && <span className="tenant-badge__name">{name}</span>}
    </span>
  );
}
