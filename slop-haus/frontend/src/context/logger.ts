import { createContext, useContext } from "react";
import type { Logger } from "@slop-factory/logging/logger";

// Split from logger_context.tsx, as auth.ts is from auth_context.tsx: a file
// that exports only the provider component keeps fast refresh working.
export const LoggerContext = createContext<Logger | null>(null);

export function useLogger(): Logger {
  const ctx = useContext(LoggerContext);
  if (!ctx) throw new Error("useLogger must be used within a LoggerProvider");
  return ctx;
}
