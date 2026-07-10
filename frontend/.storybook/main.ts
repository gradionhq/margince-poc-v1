import type { StorybookConfig } from "@storybook/react-vite";

// Storybook 9 on the react-vite builder — version-matched to this repo's
// Vite 6 / Vitest 3 toolchain (the foundation skeleton runs Storybook 10 on
// Vite 8 / Vitest 4; we stay on 9 so the existing frontend test lanes and the
// AC/axe UAT harness are untouched). Stories are the render surface the
// change-scoped fe-uat capture gate (frontend/scripts/fe-uat.mjs) drives.
const config: StorybookConfig = {
  stories: ["../src/**/*.stories.@(ts|tsx)"],
  addons: ["@storybook/addon-docs", "@storybook/addon-a11y"],
  framework: { name: "@storybook/react-vite", options: {} },
};

export default config;
