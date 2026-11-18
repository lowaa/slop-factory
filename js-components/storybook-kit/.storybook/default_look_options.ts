import { rootAttributeTheme, type DefaultStyleOptions } from "../src/default_style_check";

// The switch an app with a light/dark pair would pass: dark is a data-theme
// on the root, light is its absence.
export const DEFAULT_LOOK_OPTIONS: DefaultStyleOptions = {
  themes: [rootAttributeTheme("light", "data-theme", null), rootAttributeTheme("dark", "data-theme", "dark")],
  requireColorScheme: "light",
};
