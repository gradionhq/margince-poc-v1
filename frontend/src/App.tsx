import { extensionScreens } from "@composition/screens";
import {
  type ReactNode,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import { EXTENSION_SCREEN, findExtension } from "./app/extensions";
import { AskFab } from "./app/fab";
import {
  CommandPalette,
  useBuiltinCommands,
  usePaletteHotkey,
} from "./app/palette";
import { navigate } from "./app/router";
import { Shell, type ShellCounts, useRoute } from "./app/shell";
import { Card, EmptyState, SectionHeader } from "./design-system/atoms";
import { useT } from "./i18n";
import { AskAiScreen } from "./screens/ai";
import {
  type AuthNotice,
  AuthScreen,
  AvailabilityScreen,
  RESET_ROUTE,
} from "./screens/auth";
import { AutomationsScreen } from "./screens/automations";
import { BookingScreen } from "./screens/book";
import { ClientSurfaceScreen } from "./screens/client";
import { AuthProbeError, consumeAuthExitNotice, useMe } from "./screens/common";
import { CustomFieldsScreen } from "./screens/customfields";
import { DealScreen, DealsScreen } from "./screens/deals";
import { DedupeScreen } from "./screens/dedupe";
import { DesignScreen } from "./screens/design";
import { HomeScreen } from "./screens/home";
import { InboxScreen, usePendingApprovals } from "./screens/inbox";
import { LeadScreen, LeadsScreen } from "./screens/leads";
import { OAuthConsent } from "./screens/oauthconsent";
import { OfferScreen } from "./screens/offers";
import { OfferTemplatesScreen } from "./screens/offertemplates";
import { OnboardingScreen, useCompany } from "./screens/onboarding";
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

// #/ext/<unit> (ADR-0069) — the composed extension tier's one route into the
// SPA. The registry is generated per installation, so this arm is the SAME
// code in the vanilla tree, where every unit name misses and the honest
// not-found card renders; that lane is the default one and must never crash or
// paint a blank frame. Split out for the same complexity-budget reason as
// DealsRoute above.
//
// A unit still ships no TSX: extensions/<name>/frontend/ is an unbuilt
// capability layer that gen-composition's scan refuses on sight, and lifting
// that would mean bundling unit-authored code into the SPA. So a unit surface
// comes from one of two CORE-owned places, in this order:
//
//   1. A bespoke screen committed under src/screens/ext/ and listed in the
//      composed screen registry ("@composition/screens"). crm-demo, the
//      reference extension, is the one such screen today; its file explains
//      why it lives in core and why only the composed lane compiles it.
//   2. Otherwise the contract-derived descriptor set — the operations the
//      unit's fragments published, which is all the app can honestly say about
//      a unit nobody wrote a screen for (de, yogi, crm-hello).
//
// The registry is consulted only AFTER the descriptor resolves, so a screen
// cannot render for a unit this installation did not compose: an entry for a
// disabled unit is inert rather than a route into a surface with no server
// behind it.
function ExtensionRoute({ name }: Readonly<{ name?: string }>) {
  const t = useT();
  const unit = findExtension(name);
  if (!unit) {
    return (
      <div className="wrap narrow">
        <EmptyState>{t("ext.notFound", { name: name ?? "" })}</EmptyState>
      </div>
    );
  }
  const Screen = extensionScreens[unit.name];
  if (Screen) {
    return <Screen />;
  }
  return (
    <div className="wrap narrow">
      <SectionHeader title={unit.name} sub={t("ext.operations")} />
      <Card>
        <ul>
          {unit.verbs.map((verb) => (
            <li key={verb.operationId}>
              {verb.title} — {verb.method} {verb.route}
            </li>
          ))}
        </ul>
      </Card>
    </div>
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
    // reached from the digest/settings, not the rail (the 9-item rail is
    // canonical): the dedupe review queue (M4).
    case "dedupe":
      return <DedupeScreen />;
    // reached from Settings, not the rail — the 9-item rail is canonical
    case "products":
      return <ProductsScreen />;
    case "offers":
      return id ? <OfferScreen id={id} /> : <PendingScreen />;
    case "offer-templates":
      return <OfferTemplatesScreen />;
    // reached only via the server's redirect off GET /oauth/authorize
    // (#/oauth-consent?…&consent=<nonce>) — never a rail destination.
    case "oauth-consent":
      return <OAuthConsent />;
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
    case EXTENSION_SCREEN:
      return <ExtensionRoute name={id} />;
    default:
      return <PendingScreen />;
  }
}

// The anonymous public surfaces render without a session — their slug in the
// path is the whole address (security: [] in the contract).
const PUBLIC_SCREENS = new Set(["book", "preferences"]);

// Screens the onboarding gate must never navigate away from, beyond
// onboarding itself. The OAuth consent screen carries a single-use,
// cookie-bound nonce in the hash (armed by GET /oauth/authorize's 302); the
// gate's navigate() rewrites location.hash, which would destroy that nonce
// with nothing able to recover it — unlike an ordinary screen, there is no
// route back once this one is skipped mid-flight. This is a narrow carve-out
// for a request in flight, not a relaxation of the gate for the screen in
// general.
const ONBOARDING_GATE_EXEMPT_SCREENS: ReadonlySet<string> = new Set([
  "onboarding",
  "oauth-consent",
]);

export function App() {
  const route = useRoute();
  if (PUBLIC_SCREENS.has(route.screen)) {
    return (
      <Shell onOpenSearch={() => undefined}>
        <ScreenView screen={route.screen} id={route.id} id2={route.id2} />
      </Shell>
    );
  }
  // The emailed reset link is a bearer credential, not a session check: it
  // must reach the reset form whether or not this browser already carries a
  // live cookie, so it is routed here, ahead of AuthedApp's session gate,
  // rather than left for AuthedApp to reach only when unauthenticated (where
  // an existing session instead rendered the authenticated shell's fallback
  // for an unrecognised route).
  if (route.screen === RESET_ROUTE) {
    return (
      <RaillessFrame>
        {/* A stale or bare reset link has no token, so the embedded form
            renders as an ordinary login rather than ResetForm — and its own
            "restore the originally requested route" check (LoginForm's
            onSuccess) never fires home for a non-empty hash, which this one
            always is. Without an explicit navigate here, a successful
            sign-in from this route would leave the reader signed in but
            still looking at a login form. */}
        <AuthScreen onAuthed={() => navigate({ screen: "home" })} />
      </RaillessFrame>
    );
  }
  return <AuthedApp route={route} />;
}

// AuthGate: everything behind the session probes GET /v1/me, and the
// boundary branches on the TYPED failure (§4 of the login spec):
// 401 → login, network/5xx → connection problem, 503 → installation
// unavailable. Authentication and availability are different product
// states — a server outage must never read as "wrong password". On login
// success the screen refetches and the app renders at the route the user
// originally asked for. No redirect races: the gate owns the decision.
function AuthedApp({
  route,
}: Readonly<{ route: ReturnType<typeof useRoute> }>) {
  const me = useMe();
  // A 401 after a previously live session is an expiry (or a deliberate
  // sign-out, which useLogout marks); a 401 on first load is just "not
  // signed in" and carries no notice.
  const hadSession = useRef(false);
  const [notice, setNotice] = useState<AuthNotice>(null);
  useEffect(() => {
    if (me.data) {
      hadSession.current = true;
      setNotice(null);
      return;
    }
    if (
      me.error instanceof AuthProbeError &&
      me.error.kind === "unauthorized"
    ) {
      const exit = consumeAuthExitNotice();
      if (exit) {
        setNotice(exit);
      } else if (hadSession.current) {
        setNotice("session-expired");
      }
      hadSession.current = false;
    }
  }, [me.data, me.error]);

  // Probed only once the session is known good: an unauthenticated /company
  // would 401 and say nothing about onboarding.
  const authed = !me.isPending && !me.isError;
  const company = useCompany(authed);
  const described = company.data !== null && company.data !== undefined;

  // route.screen is a dependency on purpose: the gate must hold on every
  // navigation, not only on first load — otherwise the palette or a typed hash
  // walks straight past onboarding. ONBOARDING_GATE_EXEMPT_SCREENS is exempt
  // or this effect would fight its own destination (onboarding) or destroy a
  // request that cannot survive the rewrite (oauth-consent).
  useEffect(() => {
    if (
      authed &&
      company.isSuccess &&
      !described &&
      !ONBOARDING_GATE_EXEMPT_SCREENS.has(route.screen)
    ) {
      navigate({ screen: "onboarding", id: "company" });
    }
  }, [authed, company.isSuccess, described, route.screen]);

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
    const kind =
      me.error instanceof AuthProbeError ? me.error.kind : "connection";
    if (kind !== "unauthorized") {
      return (
        <RaillessFrame>
          <AvailabilityScreen kind={kind} onRetry={() => me.refetch()} />
        </RaillessFrame>
      );
    }
    return (
      <RaillessFrame>
        <AuthScreen
          onAuthed={async () => {
            const result = await me.refetch();
            if (result.error) {
              throw result.error;
            }
          }}
          notice={notice}
        />
      </RaillessFrame>
    );
  }

  // An installation that has not described itself has nothing for any other
  // screen to show. The gate lives here rather than on the login path because
  // a live session never passes through login — a reload would otherwise walk
  // straight past onboarding into a company that does not exist.
  if (company.isPending) {
    return (
      <RaillessFrame>
        <AuthSplash />
      </RaillessFrame>
    );
  }

  return (
    <>
      <AuthedShell onOpenSearch={() => setPaletteOpen(true)}>
        <ScreenView screen={route.screen} id={route.id} id2={route.id2} />
      </AuthedShell>
      <CommandPalette
        open={paletteOpen}
        onClose={() => setPaletteOpen(false)}
        commands={commands}
      />
      <AskFab route={route} />
    </>
  );
}

// The shell plus its live badge counts. This is a separate component so the
// approvals read mounts only once a session exists — calling it beside the
// auth probe would fire an unauthenticated request on the login path.
//
// A badge counts only what wants attention: approvals
// waiting. Tasks will join it once there is a due-count to read; until then the
// slot renders nothing rather than a fabricated number.
function AuthedShell({
  children,
  onOpenSearch,
}: Readonly<{ children: ReactNode; onOpenSearch: () => void }>) {
  const pending = usePendingApprovals();
  const counts: ShellCounts | undefined = pending.data
    ? { inbox: pending.data.data.length }
    : undefined;
  return (
    <Shell counts={counts} onOpenSearch={onOpenSearch}>
      {children}
    </Shell>
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
