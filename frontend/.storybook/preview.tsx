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
// The strip's own sheet, for the same reason and with the same failure mode: a
// story that builds a `.topbar` by class — the account block's frames do — drew
// no grid, no height, no ground and no rule until this was here, and the trail
// sat at the window's edge instead of the column's.
import "../src/app/topbar.css";
import "../src/design-system/composed.css";
// atoms.css for the same reason, and it bites hardest: `.card`, `.btn` and the
// rest are reached BY CLASS from components that import nothing from atoms.tsx
// — the module whose side-effect import loads this sheet. In the app it is
// always present; in a story whose module graph stops short of atoms.tsx it was
// not, and design-system/explain.tsx's popover rendered as unstyled text over
// the figure it was explaining. Loading it here closes that for the catalog
// rather than one story at a time.
import "../src/design-system/atoms.css";
// settings.css for the third time in this list and the same reason: the
// settings row language reaches `.settings-panel-sub` and
// `.settings-panel-commit` BY CLASS from twenty-odd card files, none of which
// imports the sheet — `settings.tsx` does, and a story that renders one card on
// its own never reaches it. Unloaded, a card's description had no interval below
// it and its commit band none above, so every settings-card story understated
// exactly the spacing those stories exist to check.
import "../src/screens/settings.css";

// Theme decorator — sets data-theme on <html>, the same mechanism the shell
// uses (src/app/shell.tsx), so a story previews in light and dark.
const withTheme: Decorator = (Story, context) => {
  document.documentElement.dataset.theme =
    (context.globals.theme as string) ?? "light";
  return <Story />;
};

// Storybook's own `layout` parameter says how a story wants to be framed, and
// two of its three values mean "not like this": `fullscreen` asks for the raw
// viewport, `centered` asks Storybook to centre the story itself. A frame that
// ignores the parameter does not just add margin — it silently repeals it. The
// shell's sidebar measures its foot against the viewport, so 2rem of frame
// clipped it on the very story written to catch that; and a 390px phone
// viewport carrying a 2rem frame on each side is a 342px phone, which is not
// a phone any reader has.
const FRAMED_BY_STORYBOOK = new Set(["fullscreen", "centered"]);

// Surface decorator — frames every story with consistent breathing room so
// the catalog reads as composed, not dumped in the canvas corner. Stories that
// declared their own framing keep it.
const withSurface: Decorator = (Story, context) => {
  if (FRAMED_BY_STORYBOOK.has(context.parameters.layout)) {
    return <Story />;
  }
  return (
    <div style={{ minHeight: "100vh", padding: "2rem" }}>
      <Story />
    </div>
  );
};

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
  // The phone width, declared ONCE for the whole catalog rather than copied into
  // every meta that wants it. Named after the RULE and not after a device: the
  // shell's bottom bar and the settings section switcher are media queries at
  // `PHONE_MAX_WIDTH` (src/app/viewport.ts), so 390px is a width that sits
  // inside that rule rather than a particular handset. A story opts in with
  // `globals: { viewport: { value: "phone" } }`.
  //
  // Storybook 9 ships the viewport tool itself, so this adds no addon.
  parameters: {
    viewport: {
      options: {
        phone: {
          name: "Phone (max 700px)",
          styles: { width: "390px", height: "844px" },
        },
      },
    },
  },
  decorators: [withSurface, withTheme],
};

export default preview;
