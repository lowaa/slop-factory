import { useEffect, useRef } from "react";
import "./prompt_history_list.scss";

interface PromptHistoryListProps {
  id: string;
  prompts: readonly string[];
  highlighted: number;
  optionId: (index: number) => string;
  onChoose: (index: number) => void;
}

// A prompt is shown as its first line; a longer one is cut by the CSS, and one
// with more lines says so itself.
export function firstLine(prompt: string): string {
  const newline = prompt.indexOf("\n");
  return newline < 0 ? prompt : `${prompt.slice(0, newline)}…`;
}

// The prompts to choose from, oldest at the top. Focus stays in the message
// box, which points at the highlighted option, so this only draws and reports
// clicks.
export default function PromptHistoryList({ id, prompts, highlighted, optionId, onChoose }: PromptHistoryListProps) {
  const current = useRef<HTMLLIElement>(null);
  useEffect(() => {
    current.current?.scrollIntoView?.({ block: "nearest" });
  }, [highlighted]);

  return (
    <ul id={id} role="listbox" aria-label="Previous prompts" className="prompt-history">
      {prompts.map((prompt, i) => (
        <li
          key={prompt}
          id={optionId(i)}
          ref={i === highlighted ? current : undefined}
          role="option"
          aria-selected={i === highlighted}
          className="prompt-history__option"
          // Keeps focus (and the cursor) in the message box.
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => onChoose(i)}
        >
          {firstLine(prompt)}
        </li>
      ))}
    </ul>
  );
}
