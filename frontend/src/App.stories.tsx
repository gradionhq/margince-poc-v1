import type { Meta, StoryObj } from "@storybook/react-vite";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "./App";
import { LocaleProvider } from "./i18n";

const meta: Meta = {
  title: "App/Shell",
  parameters: { layout: "fullscreen" },
};
export default meta;
type Story = StoryObj;

function installAppStub() {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
  // Land on a screen with a known-good empty-state story rather than Home,
  // whose dashboard queries need richer fixtures than this shell smoke test cares about.
  globalThis.location.hash = "#/products";
  globalThis.fetch = (async (input: Request | string | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/me")) {
      return new Response(
        JSON.stringify({
          user: { email: "ada@acme.test" },
          roles: ["admin"],
          teams: [],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }
    return new Response(
      JSON.stringify({
        data: [],
        page: { next_cursor: null, has_more: false },
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
  }) as typeof fetch;
}

export const AuthenticatedHome: Story = {
  render: () => {
    installAppStub();
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    return (
      <QueryClientProvider client={client}>
        <LocaleProvider>
          <App />
        </LocaleProvider>
      </QueryClientProvider>
    );
  },
};
