import type { ReactNode } from "react";
import Command from "@/shared/command/command";
import { RUN_ENV, TOKEN_SH } from "./paths";

interface WorkspaceNotesProps {
  // What is being started, as it reads in "restart the ___": "worker" or
  // "remote control".
  subject: string;
  // Notes only this page has, shown first.
  lead?: ReactNode;
  // Extra occasions for signing in again, e.g. "(also after ...)".
  signedOutHint?: ReactNode;
  // What the environment file holds, as this page uses it.
  environment: ReactNode;
}

// The advice that is the same wherever a workspace is started from: restarting
// picks up updates, how to sign in again, and where the environment lives.
export default function WorkspaceNotes({ subject, lead, signedOutHint, environment }: WorkspaceNotesProps) {
  return (
    <>
      <h3>Good to know</h3>
      <ul>
        {lead}
        <li>
          Every start updates the workspace&rsquo;s slop-haus files first, so <strong>restart</strong> the {subject} to
          pick up a new release. If the update cannot run, <code>--no-update</code> (before the harness flags) starts
          on the files as they are.
        </li>
        <li>If a start says the workspace is signed out, sign it in again{signedOutHint}:</li>
      </ul>
      <Command value={`${TOKEN_SH} login`} label="the sign-in command" />
      <ul>
        <li>
          {environment} is <code>{RUN_ENV}</code>. Updates never rewrite it.
        </li>
      </ul>
    </>
  );
}
