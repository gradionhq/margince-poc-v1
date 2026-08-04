import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { applyTheme, resolveTheme } from "./app/theme";
import { LocaleProvider } from "./i18n";
import "./app.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: 1, refetchOnWindowFocus: false },
  },
});

// BEFORE the first render, and before any authentication is known. The theme
// used to be applied by an effect inside the signed-in chrome, so every
// unauthenticated surface — sign-in, password reset, the availability screens —
// ignored a dark-mode reader entirely, and inherited a stale attribute after a
// sign-out. Setting it here also avoids a light-to-dark flash on reload for a
// reader who has chosen dark.
applyTheme(resolveTheme());

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
        <App />
      </LocaleProvider>
    </QueryClientProvider>
  </StrictMode>,
);
