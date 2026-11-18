import ThisTenantSection from "./this_tenant_section";
import ArchiveTenantSection from "./archive_tenant_section";
import StewardSection from "./steward_section";
import ReviewProviderSection from "./review_provider_section";
import YourSettingsSection from "./your_settings_section";
import "./tenant_settings_page.scss";

export default function TenantSettingsPage() {
  return (
    <div className="tenant-settings">
      <ThisTenantSection />
      <StewardSection />
      <ReviewProviderSection />
      <YourSettingsSection />
      <ArchiveTenantSection />
    </div>
  );
}
