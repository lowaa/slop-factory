import { useState, type ReactNode } from "react";
import Tabs from "@/shared/tabs/tabs";
import { DEFAULT_HARNESS, HARNESSES, harnessById, type Harness, type HarnessId } from "@/domain/harnesses";

const ITEMS = HARNESSES.map(({ id, label }) => ({ id, label }));

interface HarnessTabsProps {
  // The selected harness's panel.
  children: (harness: Harness) => ReactNode;
}

// One tab per registered harness, so a page cannot offer a different set from
// another page.
export default function HarnessTabs({ children }: HarnessTabsProps) {
  const [selected, setSelected] = useState<HarnessId>(DEFAULT_HARNESS);
  return (
    <Tabs items={ITEMS} value={selected} onChange={setSelected} label="Harness">
      {children(harnessById(selected))}
    </Tabs>
  );
}
