import { useCallback, useState } from "react";
import { AskFab } from "./app/fab";
import {
  CommandPalette,
  useBuiltinCommands,
  usePaletteHotkey,
} from "./app/palette";
import { Shell, useRoute } from "./app/shell";
import { EmptyState } from "./design-system/atoms";
import { useT } from "./i18n";
import { DesignScreen } from "./screens/design";
import { LeadScreen, LeadsScreen } from "./screens/leads";
import { CompaniesScreen, CompanyScreen } from "./screens/organizations";
import { ContactsScreen, PersonScreen } from "./screens/people";

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

function ScreenView({ screen, id }: { screen: string; id?: string }) {
  switch (screen) {
    case "design":
      return <DesignScreen />;
    case "contacts":
      return id ? <PersonScreen id={id} /> : <ContactsScreen />;
    case "companies":
      return id ? <CompanyScreen id={id} /> : <CompaniesScreen />;
    case "leads":
      return id ? <LeadScreen id={id} /> : <LeadsScreen />;
    default:
      return <PendingScreen />;
  }
}

export function App() {
  const route = useRoute();
  const [paletteOpen, setPaletteOpen] = useState(false);
  const commands = useBuiltinCommands();
  usePaletteHotkey(useCallback(() => setPaletteOpen((open) => !open), []));

  return (
    <>
      <Shell onOpenSearch={() => setPaletteOpen(true)}>
        <ScreenView screen={route.screen} id={route.id} />
      </Shell>
      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        commands={commands}
      />
      <AskFab route={route} />
    </>
  );
}
