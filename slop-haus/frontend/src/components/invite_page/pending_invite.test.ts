import { expect, it } from "vitest";
import { forgetPersonalState } from "@/lib/personal_state";
import { hasPendingInviteToken, readToken } from "./pending_invite";

it("is forgotten when the person signs out", () => {
  window.history.replaceState({}, "", "/invite#token=abc");
  readToken();
  window.history.replaceState({}, "", "/");
  expect(hasPendingInviteToken()).toBe(true);

  forgetPersonalState();

  expect(hasPendingInviteToken()).toBe(false);
});
