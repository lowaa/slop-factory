// The pages the sessions sidebar opens in the main pane, in place of a chat.
export const REMOTE_CONTROL_VIEWS = ["setup", "running", "tenant"] as const;
export type RemoteControlView = (typeof REMOTE_CONTROL_VIEWS)[number];

export function parseRemoteControlView(raw: unknown): RemoteControlView | undefined {
  return REMOTE_CONTROL_VIEWS.find((v) => v === raw);
}
