import type { Meta, StoryObj } from "@storybook/react-vite";
import { expect, userEvent, within } from "storybook/test";
import type { SignInMethods, SignInMethodsApi } from "@slop-factory/auth";
import { ApiError } from "@/api";
import { AuthContext, type AuthContextValue } from "@/context/auth";
import { makeProfile } from "@/test/fixtures";
import { WithConsent } from "@/test/with_consent";
import AccountPage from "./account_page";
import "@/app.scss";

// The page as the board shows it, on the app's own dark theme -- the
// shared panel only reads the token names index.scss aliases, so this is
// where a contrast problem between the two would show.
const auth: AuthContextValue = {
  status: "authenticated",
  profile: makeProfile({ display_name: "Ada Lovelace", email: "ada@example.com" }),
  notice: null,
  signedOutByUser: false,
  registrationOpen: false,
  passwordLogin: true,
  providers: [
    { name: "dex", display_name: "Dex" },
    { name: "github", display_name: "GitHub" },
  ],
  signIn: async () => {},
  signOut: async () => {},
};

// storyApi answers list with methods and refuses a removal of the last way
// in, as the server does.
function storyApi(methods: SignInMethods): SignInMethodsApi {
  const ways = () => (methods.password ? 1 : 0) + methods.identities.length;
  return {
    list: async () => methods,
    addPassword: async () => {},
    changePassword: async () => {},
    removePassword: async () => {
      if (ways() <= 1) throw new ApiError(409, "that is the last way in", "last_method");
    },
    removeIdentity: async () => {},
    startLink: async () => "about:blank",
    startReauth: async () => "about:blank",
    reauthWithPassword: async () => {},
  };
}

const meta = {
  title: "Pages/AccountPage",
  component: AccountPage,
  args: { profile: auth.profile! },
  decorators: [
    (Story) => (
      <AuthContext.Provider value={auth}>
        <WithConsent>
          <div className="app__main">
            <Story />
          </div>
        </WithConsent>
      </AuthContext.Provider>
    ),
  ],
} satisfies Meta<typeof AccountPage>;

export default meta;
type Story = StoryObj<typeof meta>;

export const PasswordAndDex: Story = {
  args: {
    api: storyApi({
      password: { email: "ada@example.com", verified: true },
      identities: [{ provider: "dex", subject: "sub-ada", login: "ada@example.com", usable: true }],
      passwordLoginEnabled: true,
    }),
  },
};

export const LastWayInRefused: Story = {
  args: {
    api: storyApi({ password: { email: "ada@example.com", verified: true }, identities: [], passwordLoginEnabled: true }),
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(await canvas.findByRole("button", { name: "Remove password" }));
    await userEvent.click(canvas.getByRole("button", { name: "Yes, remove" }));
    expect(await canvas.findByRole("alert")).toHaveTextContent("That’s the only way you can sign in");
  },
};
