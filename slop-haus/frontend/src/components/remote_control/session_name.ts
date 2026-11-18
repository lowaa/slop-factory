export const DEFAULT_SESSION_NAME = "Session";

// sessionName is what a session is called wherever it is listed or titled:
// the name its owner gave it, else the first line of its first message.
export function sessionName(conversation: { title: string | null; first_line: string | null }): string {
  return conversation.title?.trim() || conversation.first_line?.trim() || DEFAULT_SESSION_NAME;
}
