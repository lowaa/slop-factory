import { Dropdown } from "@slop-factory/ui";
import type { Conductor } from "@/api";

interface MachinePickerProps {
  conductors: Conductor[];
  value: string | null;
  onChange: (id: string) => void;
  label?: string;
}

export default function MachinePicker({ conductors, value, onChange, label = "Machine" }: MachinePickerProps) {
  return (
    <Dropdown
      label={label}
      value={value}
      options={conductors.map((c) => ({ value: c.id, label: c.name }))}
      onChange={onChange}
      emptyMessage="You have no remote control to use."
    />
  );
}
