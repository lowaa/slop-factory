import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import LegalPage from "./legal_page";
import type { LegalDocsApi, LegalDocument } from "./types";

const doc: LegalDocument = {
  kind: "terms",
  version: "2026-10-01",
  locale: "en",
  effective: "2026-10-01",
  material: false,
  reviewed: true,
  current: true,
  body: "# Synthetic terms\n\nPlaceholder text for the story.",
};

// A stand-in for the server, for stories only.
const api: LegalDocsApi = {
  getDocument: async () => doc,
  getHistory: async () => ({ kind: "terms", locale: "en", versions: [] }),
};

const meta: Meta<typeof LegalPage> = {
  title: "privacy/LegalPage",
  component: LegalPage,
  args: { kind: "terms", href: (kind) => `/legal/${kind}`, api },
};

export default meta;
type Story = StoryObj<typeof LegalPage>;

export const WithBackLink: Story = {
  args: { backLink: { href: "/", label: "Back home" } },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    expect(await canvas.findByRole("link", { name: "Back home" })).toHaveAttribute("href", "/");
    expect(await canvas.findByRole("heading", { name: "Synthetic terms" })).toBeInTheDocument();
  },
};
