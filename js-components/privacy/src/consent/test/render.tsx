import type { ReactElement } from "react";
import { render } from "@testing-library/react";
import { ConsentProvider } from "../consent_provider";
import type { ConsentStore } from "../consent_store";

export const documentHref = (kind: string) => `/legal/${kind}`;

// Renders inside a provider and waits for the store's first answer.
export async function renderWithConsent(store: ConsentStore, ui: ReactElement = <></>, options: { ui?: boolean } = {}) {
  const result = render(
    <ConsentProvider store={store} documentHref={documentHref} ui={options.ui}>
      {ui}
    </ConsentProvider>,
  );
  await store.load();
  return result;
}
