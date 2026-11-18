import type { CreateIntakeSource } from "@/api";

export const PRIORITIES = ["low", "normal", "high", "urgent"] as const;

export interface FormValues {
  name: string;
  adapter: CreateIntakeSource["adapter"];
  trust: CreateIntakeSource["trust"];
  auth: CreateIntakeSource["auth"];
  default_status: "open" | "preparing";
  default_priority: (typeof PRIORITIES)[number];
  max_priority: (typeof PRIORITIES)[number];
  reopen_policy: "new" | "reopen";
  resolve_closes: "off" | "on";
  roles: string;
  paths: string;
  role_match: "any" | "all";
}

export const INITIAL_FORM: FormValues = {
  name: "",
  adapter: "native",
  trust: "untrusted",
  auth: "bearer",
  default_status: "preparing",
  default_priority: "normal",
  max_priority: "high",
  reopen_policy: "new",
  resolve_closes: "off",
  roles: "",
  paths: "",
  role_match: "any",
};

export function splitList(text: string): string[] {
  return text
    .split(",")
    .map((part) => part.trim())
    .filter((part) => part !== "");
}

export function toRequest(form: FormValues): CreateIntakeSource {
  const roles = splitList(form.roles);
  return {
    name: form.name.trim(),
    adapter: form.adapter,
    trust: form.trust,
    auth: form.auth,
    // The server forces preparing for an untrusted source anyway; sending
    // what the form shows keeps the two in agreement.
    default_status:
      form.trust === "untrusted" ? "preparing" : form.default_status,
    default_priority: form.default_priority,
    max_priority: form.max_priority,
    reopen_policy: form.reopen_policy,
    // The server refuses it for an untrusted source; sending what the form
    // shows keeps the two in agreement.
    resolve_closes: form.trust === "trusted" && form.resolve_closes === "on",
    roles,
    paths: splitList(form.paths),
    role_match: roles.length > 0 ? form.role_match : "any",
  };
}
