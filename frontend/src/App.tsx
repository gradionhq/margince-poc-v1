import { useState } from "react";
import { Shell, useRoute } from "./app/shell";
import { EmptyState } from "./design-system/atoms";
import { useT } from "./i18n";
import { DesignScreen } from "./screens/design";

// Route → screen. Surfaces land here ticket by ticket; anything not yet
// built renders the honest pending state, never a blank page.

function PendingScreen() {
  const t = useT();
  return (
    <div className="wrap narrow">
      <EmptyState>{t("screen.pending")}</EmptyState>
    </div>
  );
}

function ScreenView({ screen }: { screen: string }) {
  switch (screen) {
    case "design":
      return <DesignScreen />;
    default:
      return <PendingScreen />;
  }
}

export function App() {
  const route = useRoute();
  // The palette (B-EP09.5) mounts here; until it lands, the search affordance
  // exists but opens nothing — state is wired so 09.5 is a drop-in.
  const [, setPaletteOpen] = useState(false);

  return (
    <Shell onOpenSearch={() => setPaletteOpen(true)}>
      <ScreenView screen={route.screen} />
    </Shell>
  );
}
