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
import { OfferScreen } from "./screens/offers";
import { OfferTemplatesScreen } from "./screens/offertemplates";
import { OnboardingScreen } from "./screens/onboarding";
import { CompaniesScreen, CompanyScreen } from "./screens/organizations";
import { PartnersScreen } from "./screens/partners";
import { ContactsScreen, PersonScreen } from "./screens/people";
import { PreferenceCenterScreen } from "./screens/preferences";
import { ProductsScreen } from "./screens/products";
import { ReportsScreen } from "./screens/reports";
import { SearchScreen } from "./screens/search";
import { SettingsScreen } from "./screens/settings";
import { ShareScreen } from "./screens/share";
import { TasksScreen } from "./screens/tasks";

// Route → screen. Surfaces land here ticket by ticket; anything not yet
// built renders the honest pending state, never a blank page.

// safeDecode tolerates malformed percent-encoding (e.g. a stray "%2" from a
// hand-edited hash route): decodeURIComponent throws a URIError on bad
// escapes, and a route param is untrusted input, so a decode failure falls
// back to the raw string rather than crashing the render.
function safeDecode(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

function PendingScreen() {
  const t = useT();
  return (
    <div className="wrap narrow">
      <EmptyState>{t("screen.pending")}</EmptyState>
    </div>
  );
}

// Split out of ScreenView's switch purely to keep that function's cognitive
// complexity under the lint ceiling — the deals list/detail split has its
// own "new" vs existing-id branch that would otherwise count twice.
function DealsRoute({ id }: Readonly<{ id?: string }>) {
  return id && id !== "new" ? (
    <DealScreen id={id} />
  ) : (
    <DealsScreen startCreating={id === "new"} />
  );
}

// #/share/<record_type>/<record_id> (AS-3/4/5) — both segments are required;
// a bare #/share renders the honest pending state instead of a screen with
// nothing to share. Split out for the same complexity-budget reason as
// DealsRoute above.
function ShareRoute({ id, id2 }: Readonly<{ id?: string; id2?: string }>) {
  return id && id2 ? (
    <ShareScreen recordType={id} recordId={id2} />
  ) : (
    <PendingScreen />
  );
}

function ScreenView({
  screen,
  id,
  id2,
}: Readonly<{ screen: string; id?: string; id2?: string }>) {
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
      return <DealsRoute id={id} />;
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
    case "products":
      return <ProductsScreen />;
    case "offers":
      return id ? <OfferScreen id={id} /> : <PendingScreen />;
    case "offer-templates":
      return <OfferTemplatesScreen />;
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
    case "preferences":
      // #/preferences/<token> — anonymous; the token in the path is the
      // whole capability (security: [] in the contract).
      return <PreferenceCenterScreen token={id} />;
    case "share":
      return <ShareRoute id={id} id2={id2} />;
    case "search":
      return <SearchScreen q={id ? safeDecode(id) : ""} />;
    default:
      return <PendingScreen />;
  }
}

// The anonymous public surfaces render without a session — their slug in the
// path is the whole address (security: [] in the contract).
const PUBLIC_SCREENS = new Set(["book", "preferences"]);

export function App() {
  const route = useRoute();
  if (PUBLIC_SCREENS.has(route.screen)) {
    return (
      <Shell onOpenSearch={() => undefined}>
        <ScreenView screen={route.screen} id={route.id} id2={route.id2} />
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
        <ScreenView screen={route.screen} id={route.id} id2={route.id2} />
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
