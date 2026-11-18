import RichText from "@/shared/rich_text/rich_text";
import type { ActivityCall, AssistantPart } from "@/domain/transcript";

// The mark of a call's state is text as well as a symbol, for readers who
// cannot tell the symbols apart.
const STATUS_LABEL: Record<ActivityCall["status"], string> = { running: "running", done: "done", failed: "failed" };

interface Props {
  parts: AssistantPart[];
  done: boolean;
}

// One reply: each model message is its own paragraph, and the tool calls
// between them are a compact list that is open while the turn runs and folds
// away when it ends.
export default function AssistantReply({ parts, done }: Props) {
  return (
    <>
      {parts.map((part, i) =>
        part.type === "text" ? (
          <RichText key={i} className="conversation__text conversation__block" text={part.text} />
        ) : (
          <details key={i} className="conversation__activity" open={!done}>
            <summary>{part.calls.length === 1 ? "1 tool call" : `${part.calls.length} tool calls`}</summary>
            <ul>
              {part.calls.map((call) => (
                <li key={call.id} className={`conversation__call conversation__call--${call.status}`}>
                  <span className="conversation__call-tool">{call.tool}</span>
                  {call.line !== "" && <span className="conversation__call-line">{call.line}</span>}
                  <span className="conversation__call-status">{STATUS_LABEL[call.status]}</span>
                </li>
              ))}
            </ul>
          </details>
        ),
      )}
    </>
  );
}
