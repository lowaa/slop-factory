import type { ReactNode } from "react";
import { useAuthPages } from "../config";
import "./auth_layout.scss";

interface AuthLayoutProps {
  title: string;
  subtitle?: ReactNode;
  children: ReactNode;
}

// The centred card every sign-in-adjacent screen sits in: the five auth
// pages here, but also an app's own invite/authorize/device-code-style
// screens and an admin bundle's "sign in to continue" prompt. Brand comes
// from AuthPagesProvider's config rather than a prop -- every screen that
// renders this card already sits inside that provider (an app wraps its
// whole tree in one, precisely so screens like those can reach it too), so
// a prop would just be a second way to say the same thing.
export default function AuthLayout({ title, subtitle, children }: AuthLayoutProps) {
  const { brand, layoutClassName } = useAuthPages();
  return (
    <div className={layoutClassName ? `auth-layout ${layoutClassName}` : "auth-layout"}>
      <div className="auth-layout__card">
        <div className="auth-layout__brand">{brand}</div>
        <h1 className="auth-layout__title">{title}</h1>
        {subtitle && <p className="auth-layout__subtitle">{subtitle}</p>}
        {children}
      </div>
    </div>
  );
}

export type AuthNoticeVariant = "error" | "success" | "info";

interface AuthNoticeProps {
  variant: AuthNoticeVariant;
  children: ReactNode;
}

// role="alert" only for errors -- a success or informational notice isn't
// the kind of interruption a screen reader needs announced the moment it
// appears, but a request that failed is.
export function AuthNotice({ variant, children }: AuthNoticeProps) {
  return (
    <p
      className={`auth-layout__notice auth-layout__notice--${variant}`}
      role={variant === "error" ? "alert" : "status"}
    >
      {children}
    </p>
  );
}
