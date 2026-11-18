import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, within } from "storybook/test";
import MarkdownView from "./markdown_view";

const meta = {
  title: "Widgets/MarkdownView",
  component: MarkdownView,
  decorators: [
    (Story) => (
      <div style={{ maxWidth: 480, padding: 16 }}>
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof MarkdownView>;

export default meta;
type Story = StoryObj<typeof meta>;

const SAMPLE = `# Heading
Some **bold**, _italic_, ~~struck~~ text and \`inline code\`, with a [link](https://example.com).

- [x] done
- [ ] to do

| a | b |
|---|---|
| 1 | 2 |

\`\`\`
const a = 1;
\`\`\`

![a diagram](https://example.com/d.png)
`;

export const Default: Story = { args: { text: SAMPLE } };
export const Plain: Story = { args: { text: SAMPLE, plain: true } };

export const HostileInput: Story = {
  args: {
    text: [
      "<script>window.__pwned = true</script>",
      '<img src=x onerror="window.__pwned = true">',
      "[click](javascript:alert(1))",
      "![track](https://tracker.example/pixel.gif)",
    ].join("\n\n"),
  },
  play: async ({ canvasElement }) => {
    expect(canvasElement.querySelector("script, img, [onerror]")).toBeNull();
    expect(within(canvasElement).queryByRole("link", { name: "click" })).toBeNull();
    expect(within(canvasElement).getByRole("link", { name: "track" })).toBeInTheDocument();
  },
};
