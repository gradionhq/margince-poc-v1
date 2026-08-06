import { QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { api } from "./api/client";
import { AppErrorBoundary } from "./app/errorboundary";
import { createQueryClient } from "./app/queryclient";
import { applyTheme, resolveTheme } from "./app/theme";
import { LocaleProvider } from "./i18n";
import "./app.css";

const queryClient = createQueryClient();

// BEFORE the first render, and before any authentication is known. The theme
// used to be applied by an effect inside the signed-in chrome, so every
// unauthenticated surface — sign-in, password reset, the availability screens —
// ignored a dark-mode reader entirely, and inherited a stale attribute after a
// sign-out. Setting it here also avoids a light-to-dark flash on reload for a
// reader who has chosen dark.
applyTheme(resolveTheme());

// A 403 is the server disagreeing with the capability snapshot the UI is
// rendering from — either the grants changed under a live session (a role
// change revokes nothing, so the session survives it) or the snapshot was
// wrong. Either way the cached answer is stale the moment it is contradicted,
// so refetch it rather than let the offending control sit there until the tab
// next takes focus.
//
// Registered here because this is where the query client lives; the API module
// itself owns no cache. Only /me is invalidated — a 403 says nothing about the
// freshness of the data queries.
api.use({
  onResponse({ response }) {
    // Never on /me's OWN 403. Invalidating the query that produced the response
    // refetches it, which 403s again — a request loop in place of a stable
    // error. (A dead session answers 401 here, not 403, so this path is the
    // unusual one; it still must not spin.)
    if (
      response.status === 403 &&
      !new URL(response.url).pathname.endsWith("/me")
    ) {
      void queryClient.invalidateQueries({ queryKey: ["me"] });
    }
    return response;
  },
});

if (import.meta.env.PROD && "serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js");
}

const root = document.getElementById("root");
if (!root) {
  throw new Error("index.html must provide #root");
}
createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <LocaleProvider>
        {/* Inside the locale provider: the fallback is translated copy like
            every other user-facing string, so it cannot render above the
            catalog it reads from. */}
        <AppErrorBoundary>
          <App />
        </AppErrorBoundary>
      </LocaleProvider>
    </QueryClientProvider>
  </StrictMode>,
);
