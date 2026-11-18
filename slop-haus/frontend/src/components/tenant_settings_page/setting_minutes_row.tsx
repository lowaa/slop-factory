import SettingNumberRow from "./setting_number_row";

type SettingMinutesRowProps = Omit<
  React.ComponentProps<typeof SettingNumberRow>,
  "readOnlyNote"
>;

// A number row whose unit defaults to minutes.
export default function SettingMinutesRow({
  unit = "minutes",
  ...rest
}: SettingMinutesRowProps) {
  return <SettingNumberRow unit={unit} {...rest} />;
}
