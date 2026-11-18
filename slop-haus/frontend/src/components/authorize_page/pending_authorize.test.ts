import { expect, it } from "vitest";
import { forgetPersonalState } from "@/lib/personal_state";
import { captureAuthorizeRequestId, hasPendingAuthorizeRequest } from "./pending_authorize";

it("is forgotten when the person signs out", () => {
  window.history.replaceState({}, "", "/authorize?request=req-1");
  captureAuthorizeRequestId();
  window.history.replaceState({}, "", "/");
  expect(hasPendingAuthorizeRequest()).toBe(true);

  forgetPersonalState();

  expect(hasPendingAuthorizeRequest()).toBe(false);
});
