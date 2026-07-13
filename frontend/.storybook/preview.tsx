import type { Decorator, Preview } from "@storybook/react-vite";
// app.css loads Tailwind + the Ledger-Green tokens + base element styles, so
// every story renders on the real design-system surface with each var
// resolved. No `backgrounds` palette is configured: the addon needs literal
// colours, which ds-purity bans — the theme switch below is a decorator.
import "../src/app.css";
// Structural chrome (.wrap/.list-head/.list-toolbar) and composed surfaces
// (.card/.firmo/.meterbar/…) live in these two sheets, loaded in the real
// app via component-colocated side-effect imports (app/shell.tsx,
// design-system/composed.tsx) that most stories never reach — importing
// them here keeps story renders matching production chrome.
import "../src/app/shell.css";
import "../src/design-system/composed.css";

// Theme decorator — sets data-theme on <html>, the same mechanism the shell
// uses (src/app/shell.tsx), so a story previews in light and dark.
const withTheme: Decorator = (Story, context) => {
  document.documentElement.dataset.theme =
    (context.globals.theme as string) ?? "light";
  return <Story />;
};

// Surface decorator — frames every story with consistent breathing room so
// the catalog reads as composed, not dumped in the canvas corner.
const withSurface: Decorator = (Story) => (
  <div style={{ minHeight: "100vh", padding: "2rem" }}>
    <Story />
  </div>
);

const preview: Preview = {
  globalTypes: {
    theme: {
      description: "Ledger-Green theme",
      defaultValue: "light",
      toolbar: {
        title: "Theme",
        icon: "circlehollow",
        items: [
          { value: "light", title: "Light" },
          { value: "dark", title: "Dark" },
        ],
        dynamicTitle: true,
      },
    },
  },
  decorators: [withSurface, withTheme],
};

export default preview;
