import CopyButton from "@/shared/copy_button/copy_button";
import "./command.scss";

// A command to paste into a terminal, with a button that copies it intact.
export default function Command({ value, label }: { value: string; label: string }) {
  return (
    <div className="command">
      <pre>{value}</pre>
      <CopyButton value={value} label={label} />
    </div>
  );
}
