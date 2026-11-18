// Finds form controls that still look like the browser's default.
//
// Each control is compared with a bare element of the same tag and type,
// reverted to the user-agent stylesheet, so there is no table of "default"
// values to keep in step with the browser. A control whose compared
// properties all equal the bare element's has had no styling applied.
//
// This file imports nothing: an e2e suite compiles it on its own and injects
// it into a page, so it has to stand alone.

const CONTROL_SELECTOR = "button, input, select, textarea";

// Input types that draw no control of their own.
const NON_VISUAL_INPUT_TYPES = new Set(["hidden"]);

const SIDES = ["top", "right", "bottom", "left"] as const;

const COMPARED_PROPERTIES: readonly string[] = [
  ...SIDES.flatMap((side) => [`border-${side}-width`, `border-${side}-style`, `border-${side}-color`]),
  "border-top-left-radius",
  "border-top-right-radius",
  "border-bottom-right-radius",
  "border-bottom-left-radius",
  "background-color",
  ...SIDES.map((side) => `padding-${side}`),
];

// Only a checkbox or radio is drawn from accent-color.
const ACCENT_TYPES = new Set(["checkbox", "radio"]);

// A control meant to look native says why: the attribute's value is the
// reason, and an empty one does not opt out.
export const NATIVE_LOOK_ATTRIBUTE = "data-native-look";

// How an app puts the page into one of its themes. `apply` returns the
// function that puts the page back. The kit does not know how any app
// switches theme (a data attribute, a class, a media query), so the app
// lists its themes; with none listed the page is checked as it is.
export interface ThemeSwitch {
  name: string;
  apply(): () => void;
}

export interface DefaultStyleOptions {
  themes?: readonly ThemeSwitch[];
  // Fail unless the browser prefers this colour scheme. For an app whose
  // "light" theme is just "no theme set", which is only light when the
  // browser doesn't prefer dark: without the check a dark-preferring run
  // would pass while only ever having checked dark.
  requireColorScheme?: "light" | "dark";
}

export const CURRENT_THEME = "current";

const CURRENT_THEME_ONLY: readonly ThemeSwitch[] = [{ name: CURRENT_THEME, apply: () => () => {} }];

// A theme chosen by an attribute on the root element; a null value is the
// theme with the attribute absent.
export function rootAttributeTheme(name: string, attribute: string, value: string | null): ThemeSwitch {
  return {
    name,
    apply() {
      const root = document.documentElement;
      const previous = root.getAttribute(attribute);
      if (value === null) {
        root.removeAttribute(attribute);
      } else {
        root.setAttribute(attribute, value);
      }
      return () => {
        if (previous === null) {
          root.removeAttribute(attribute);
        } else {
          root.setAttribute(attribute, previous);
        }
      };
    },
  };
}

export interface DefaultStyledControl {
  element: HTMLElement;
  description: string;
  theme: string;
}

function controlType(element: HTMLElement): string | null {
  return element instanceof HTMLInputElement || element instanceof HTMLButtonElement ? element.type : null;
}

function describe(element: HTMLElement): string {
  const labels = "labels" in element ? (element.labels as NodeListOf<HTMLLabelElement> | null) : null;
  const tag = element.tagName.toLowerCase();
  const type = controlType(element);
  const label =
    element.getAttribute("aria-label") ??
    labels?.[0]?.textContent?.trim() ??
    element.textContent?.trim().slice(0, 40) ??
    "";
  const classes =
    element.className && typeof element.className === "string"
      ? `.${element.className.trim().split(/\s+/).join(".")}`
      : "";
  return `${tag}${type ? `[type=${type}]` : ""}${classes} "${label}"`;
}

function isOptedOut(element: HTMLElement): boolean {
  return (element.getAttribute(NATIVE_LOOK_ATTRIBUTE) ?? "").trim() !== "";
}

function isVisible(element: HTMLElement): boolean {
  if (element instanceof HTMLInputElement && NON_VISUAL_INPUT_TYPES.has(element.type)) {
    return false;
  }
  return element.checkVisibility();
}

function bareTwin(element: HTMLElement): HTMLElement {
  const twin = document.createElement(element.tagName);
  // The compared properties are rolled back to the user-agent stylesheet, so
  // the twin is the browser's default whatever the page's element-level rules
  // say. Without it a globally styled control would match its twin and be
  // reported as unstyled. Not `all: revert`: that would also reset `color`,
  // which a border's currentColor resolves against, and every control would
  // then differ.
  for (const property of [...COMPARED_PROPERTIES, "accent-color"]) {
    twin.style.setProperty(property, "revert");
  }
  const type = controlType(element);
  if (type !== null) {
    (twin as HTMLInputElement | HTMLButtonElement).type = type;
  }
  return twin;
}

function differs(control: HTMLElement, twin: HTMLElement, properties: readonly string[]): boolean {
  const controlStyle = getComputedStyle(control);
  const twinStyle = getComputedStyle(twin);
  return properties.some(
    (property) => controlStyle.getPropertyValue(property) !== twinStyle.getPropertyValue(property),
  );
}

function assertColorScheme(scheme: "light" | "dark" | undefined): void {
  if (scheme !== undefined && !window.matchMedia(`(prefers-color-scheme: ${scheme})`).matches) {
    throw new Error(
      `default-look check needs a browser that prefers the ${scheme} scheme, or its ${scheme} pass would check the other theme`,
    );
  }
}

export async function withThemeAsync<T>(theme: ThemeSwitch, run: () => Promise<T>): Promise<T> {
  const restore = theme.apply();
  try {
    return await run();
  } finally {
    restore();
  }
}

function withTheme<T>(theme: ThemeSwitch, run: () => T): T {
  const restore = theme.apply();
  try {
    return run();
  } finally {
    restore();
  }
}

// Returns the visible controls in `container` that look like the browser's
// default in any of the options' themes. The caller decides what to do with
// them.
export function findDefaultStyledControls(
  container: HTMLElement,
  options: DefaultStyleOptions = {},
): DefaultStyledControl[] {
  assertColorScheme(options.requireColorScheme);
  return (options.themes ?? CURRENT_THEME_ONLY).flatMap((theme) =>
    withTheme(theme, () => findInCurrentTheme(container, theme.name)),
  );
}

// The check in the theme the page is already in, for a caller that has put the
// page in a theme itself (the e2e suite emulates prefers-color-scheme).
export function findInCurrentTheme(container: HTMLElement, theme: string = CURRENT_THEME): DefaultStyledControl[] {
  const found: DefaultStyledControl[] = [];
  for (const element of container.querySelectorAll<HTMLElement>(CONTROL_SELECTOR)) {
    if (isOptedOut(element) || !isVisible(element)) {
      continue;
    }
    const twin = bareTwin(element);
    // Under body, not beside the control, so component-scoped selectors
    // (`.field input { ... }`) don't reach the twin through its container.
    document.body.append(twin);
    try {
      const type = controlType(element);
      const properties =
        type !== null && ACCENT_TYPES.has(type) ? [...COMPARED_PROPERTIES, "accent-color"] : COMPARED_PROPERTIES;
      if (!differs(element, twin, properties)) {
        found.push({ element, description: describe(element), theme });
      }
    } finally {
      twin.remove();
    }
  }
  return found;
}

export function formatDefaultStyledControls(controls: readonly DefaultStyledControl[]): string {
  return [
    `${controls.length} control(s) use the browser's default look:`,
    ...controls.map(
      (control) => `  - ${control.description}${control.theme === CURRENT_THEME ? "" : ` (${control.theme} theme)`}`,
    ),
    `Style them, or mark one meant to look native with ${NATIVE_LOOK_ATTRIBUTE}="<reason>" (or the story parameter defaultStyle: { skip: "<reason>" }).`,
  ].join("\n");
}

// What a Storybook preview runs after each story. `skip` is the story's
// defaultStyle.skip parameter.
export function assertNoDefaultStyledControls(
  container: HTMLElement,
  skip: string | undefined,
  options: DefaultStyleOptions = {},
): void {
  if (skip !== undefined) {
    if (skip.trim() === "") {
      throw new Error("parameters.defaultStyle.skip needs a reason");
    }
    return;
  }
  const controls = findDefaultStyledControls(container, options);
  if (controls.length > 0) {
    throw new Error(formatDefaultStyledControls(controls));
  }
}
