import type { ButtonHTMLAttributes } from "react";
import "./button.scss";

// primary: the one main action on a screen -- the only filled red.
// secondary: every other action with a visible boundary.
// ghost: low-emphasis actions inside dense rows (reorder, remove icons).
// tonal: actions that should stand out, possibly several on one screen,
// without competing with the one filled primary: a tinted background with
// accent text and border, not a fill.
// danger: destructive actions; red text rather than a red fill, so a list
// of rows each with a Delete doesn't turn into a column of red blocks.
export type ButtonVariant = "primary" | "secondary" | "tonal" | "ghost" | "danger";
export type ButtonSize = "sm" | "md";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
}

export default function Button({
  variant = "secondary",
  size = "md",
  // A plain <button> defaults to "submit", which makes every unlabelled
  // button inside a form submit it. Callers opt in to submit explicitly.
  type = "button",
  className,
  ...rest
}: ButtonProps) {
  const classes = ["button", `button--${variant}`, `button--${size}`, className].filter(Boolean).join(" ");
  return <button type={type} className={classes} {...rest} />;
}
