import { type ReactNode, useCallback, useState } from "react";
import { AskFab } from "./app/fab";
import {
  CommandPalette,
  useBuiltinCommands,
  usePaletteHotkey,
} from "./app/palette";
import { Shell, useRoute } from "./app/shell";
import { EmptyState } from "./design-system/atoms";
import { useT } from "./i18n";
import { AskAiScreen } from "./screens/ai";
import { AuthScreen } from "./screens/auth";
import { AutomationsScreen } from "./screens/automations";
import { BookingScreen } from "./screens/book";
import { ClientSurfaceScreen } from "./screens/client";
import { useMe } from "./screens/common";
import { CustomFieldsScreen } from "./screens/customfields";
import { DealScreen, DealsScreen } from "./screens/deals";
import { DesignScreen } from "./screens/design";
import { HomeScreen } from "./screens/home";
import { InboxScreen } from "./screens/inbox";
import { LeadScreen, LeadsScreen } from "./screens/leads";
import { OnboardingScreen } from "./screens/onboarding";
import { CompaniesScreen, CompanyScreen } from "./screens/organizations";
import { PartnersScreen } from "./screens/partners";
import { ContactsScreen, PersonScreen } from "./screens/people";
import { ReportsScreen } from "./screens/reports";
import { SettingsScreen } from "./screens/settings";
import { TasksScreen } from "./screens/tasks";

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

function ScreenView({ screen, id }: Readonly<{ screen: string; id?: string }>) {
  switch (screen) {
    case "design":
      return <DesignScreen />;
    case "contacts":
      return id ? <PersonScreen id={id} /> : <ContactsScreen />;
    case "companies":
      return id ? <CompanyScreen id={id} /> : <CompaniesScreen />;
    case "partners":
      return <PartnersScreen />;
    case "leads":
      return id ? <LeadScreen id={id} /> : <LeadsScreen />;
    case "deals":
      return id && id !== "new" ? (
        <DealScreen id={id} />
      ) : (
        <DealsScreen startCreating={id === "new"} />
      );
    case "home":
      return <HomeScreen />;
    case "inbox":
      return <InboxScreen />;
    case "tasks":
      return <TasksScreen />;
    case "reports":
      return <ReportsScreen />;
    case "ai":
      return <AskAiScreen />;
    case "settings":
      return <SettingsScreen tab={id} />;
    // reached from Settings, not the rail — the 9-item rail is canonical
    case "automations":
      return <AutomationsScreen />;
    // also reached from Settings, not the rail (AC-custom-fields admin door)
    case "custom-fields":
      return <CustomFieldsScreen />;
    case "onboarding":
      return <OnboardingScreen />;
    case "client":
      return <ClientSurfaceScreen />;
    case "book":
      // #/book/<host_slug> is the anonymous public variant
      return <BookingScreen hostSlug={id} />;
    default:
      return <PendingScreen />;
  }
}

// The anonymous public surfaces render without a session — their slug in the
// path is the whole address (security: [] in the contract).
const PUBLIC_SCREENS = new Set(["book"]);

export function App() {
  const route = useRoute();
  if (PUBLIC_SCREENS.has(route.screen)) {
    return (
      <Shell onOpenSearch={() => undefined}>
        <ScreenView screen={route.screen} id={route.id} />
      </Shell>
    );
  }
  return <AuthedApp route={route} />;
}

// AuthGate: everything behind the session probes GET /v1/me. A first-time user
// has no session (and maybe no workspace slug) — either way /me is not 200, so
// we show the signup/login screen. On success the screen refetches and the app
// renders. No redirect races: the gate owns the authenticated/not decision.
function AuthedApp({
  route,
}: Readonly<{ route: ReturnType<typeof useRoute> }>) {
  const me = useMe();

  const [paletteOpen, setPaletteOpen] = useState(false);
  const commands = useBuiltinCommands();
  usePaletteHotkey(useCallback(() => setPaletteOpen((open) => !open), []));

  if (me.isPending) {
    return (
      <RaillessFrame>
        <AuthSplash />
      </RaillessFrame>
    );
  }
  if (me.isError) {
    return (
      <RaillessFrame>
        <AuthScreen onAuthed={() => me.refetch()} />
      </RaillessFrame>
    );
  }

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

// The rail-less page frame (same shape Shell renders for onboarding/booking),
// so the pre-session screens get the app background and scroll container.
function RaillessFrame({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <div className="app railless">
      <main className="main">
        <div className="scroll">{children}</div>
      </main>
    </div>
  );
}

function AuthSplash() {
  const t = useT();
  return (
    <div className="wrap narrow ob-top">
      <EmptyState>{t("auth.checking")}</EmptyState>
    </div>
  );
}
