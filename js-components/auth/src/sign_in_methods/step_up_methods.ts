import { useEffect, useState } from "react";
import type { LoginProvider } from "../config";
import type { SignInMethods } from "./types";

// What an account can confirm itself with: a step-up only accepts the
// signed-in account's own password or identity, so offering anything else
// sends the person round a provider only to be refused.
export interface StepUpMethods {
  withPassword: boolean;
  providers: LoginProvider[];
}

// stepUpMethodsOf narrows the configured providers to those the account has
// a usable identity at, and the password to one it has that can sign in.
export function stepUpMethodsOf(methods: SignInMethods, configured: LoginProvider[]): StepUpMethods {
  return {
    withPassword: !!methods.password?.verified && methods.passwordLoginEnabled,
    providers: configured.filter((p) => methods.identities.some((i) => i.provider === p.name && i.usable)),
  };
}

export type StepUpMethodsState =
  | { status: "loading" }
  | { status: "error" }
  | ({ status: "loaded" } & StepUpMethods);

// useStepUpMethods loads the signed-in account's step-up methods through
// load, which an app backs with its own endpoint (and may build with
// stepUpMethodsOf). load is read once per mount.
export function useStepUpMethods(load: () => Promise<StepUpMethods>): StepUpMethodsState {
  const [state, setState] = useState<StepUpMethodsState>({ status: "loading" });
  useEffect(() => {
    let cancelled = false;
    load().then(
      (m) => !cancelled && setState({ status: "loaded", ...m }),
      () => !cancelled && setState({ status: "error" }),
    );
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  return state;
}
