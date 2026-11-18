import { useTenants } from "@/lib/shared_resources";
import { useTenantFavicon } from "@/lib/tenant_favicon";
import { useTenantTitlePrefix } from "@/lib/use_document_title";
import { tenantColour } from "@/lib/tenant_colour";
import { TenantBadge } from "@/components/tenant_badge";
import "./tenant_identity.scss";

// TenantIdentity tells the person which tenant they are in: a stripe across
// the top of the header and a badge with the tenant's name. Both show for a
// person in more than one tenant, where mistaking one for another is the
// risk. A person in one tenant sees nothing, unless its owner chose a
// colour: that is a choice to be seen, so the stripe shows alone.
//
// The browser tab (title prefix, tinted icon) shows the same identity.
//
// badge={false} leaves the name to the tenant switcher beside it, whose
// button already shows this tenant's colour, initials and name: the desktop
// header has no room for the same name twice.
//
// A failed fetch shows nothing: an identity marker is not worth an error in
// the header.
export default function TenantIdentity({ compact = false, badge = true }: { compact?: boolean; badge?: boolean }) {
  // Quiet on failure: the identity is decoration; without the list the
  // fallback shows.
  const tenants = useTenants().data ?? [];

  const current = tenants.find((t) => t.current);
  const several = tenants.length > 1;
  const shown = current && (several || current.colour !== null) ? current : null;
  const colour = shown ? tenantColour(shown) : null;

  // The tab strip is where tenants get mixed up, so it follows the same rule
  // as the header: named and tinted exactly when the header is.
  useTenantTitlePrefix(shown?.name ?? null);
  useTenantFavicon(colour);

  if (!shown || colour === null) return null;
  return (
    <>
      <div className="tenant-stripe" data-colour={colour} aria-hidden="true" />
      {several && badge && <TenantBadge name={shown.name} colour={colour} compact={compact} />}
    </>
  );
}
