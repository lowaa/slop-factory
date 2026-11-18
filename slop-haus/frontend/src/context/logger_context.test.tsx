import { afterEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { LoggerProvider } from "./logger_context";

const SESSION_ID_KEY = "slop-factory-logging:session-id";

describe("LoggerProvider", () => {
  afterEach(() => sessionStorage.clear());

  it("clears a stored session id when consent is withdrawn, without any record being emitted", () => {
    sessionStorage.setItem(SESSION_ID_KEY, "stored-id");
    let consented = true;
    let notify: () => void = () => {};
    const subscribe = (listener: () => void) => {
      notify = listener;
      return () => {};
    };
    render(
      <LoggerProvider hasAnalyticsConsent={() => consented} subscribeToConsent={subscribe}>
        <div />
      </LoggerProvider>,
    );

    notify();
    expect(sessionStorage.getItem(SESSION_ID_KEY)).toBe("stored-id");

    consented = false;
    notify();
    expect(sessionStorage.getItem(SESSION_ID_KEY)).toBeNull();
  });

  it("stops listening for consent changes on unmount", () => {
    const unsubscribe = vi.fn();
    const { unmount } = render(
      <LoggerProvider subscribeToConsent={() => unsubscribe}>
        <div />
      </LoggerProvider>,
    );
    unmount();
    expect(unsubscribe).toHaveBeenCalledOnce();
  });
});
