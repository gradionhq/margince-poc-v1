import { extensionScreens as composedScreens } from "@composition/screens";
import { useQuery } from "@tanstack/react-query";
import {
  lazy,
  type ReactNode,
  Suspense,
  useCallback,
  useDeferredValue,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { AgentTaskbar } from "./app/agenttaskbar";
import {
  EXTENSION_SCREEN,
  type ExtensionScreenRegistry,
  findExtension,
} from "./app/extensions";
import { AskFab } from "./app/fab";
import {
  CommandPalette,
  useBuiltinCommands,
  usePaletteHotkey,
} from "./app/palette";
import { navigate, type Screen } from "./app/router";
import { Shell, type ShellCounts, useRoute } from "./app/shell";
import { uiPreviewTaskbarEnabled } from "./app/ui-preview";
import { Card, EmptyState, SectionHeader } from "./design-system/atoms";
import { useT } from "./i18n";
import type { MessageKey } from "./i18n/en";
import {
  type AuthNotice,
  AuthScreen,
  AvailabilityScreen,
  RESET_ROUTE,
} from "./screens/auth";
import { AuthProbeError, consumeAuthExitNotice, useMe } from "./screens/common";
import { ForcedPasswordChangeScreen } from "./screens/forcedpassword";
import { InboxScreen, usePendingApprovals } from "./screens/inbox";
import { OnboardingScreen, useCompany } from "./screens/onboarding";
import { isPersonTab } from "./screens/persontab";
import { fetchSetupStatus, SetupClaimScreen } from "./screens/setupclaim";

// Route → screen. The table below is TOTAL over `Screen` (app/router.tsx), so
// every address the product answers has a view and an address it does not answer
// is a not-found page rather than a surface that reads as unbuilt.

// The routed screens arrive as their own chunks, so a visitor who has only
// reached the login form downloads the form rather than every screen in the
// product: a screen's code arrives when somebody navigates to it.
//
// What stays STATIC is what a first paint genuinely needs — the shell, the auth
// surfaces, the router — plus the two screens whose hooks the shell itself calls
// (screens/inbox's approval count, screens/onboarding's company probe). A module
// that is imported statically anywhere lands in the entry chunk whatever a
// dynamic import elsewhere says, so splitting those two would cost a round-trip
// and save nothing.
// Every route chunk, registered as it is declared. A screen is split so the
// login screen does not carry the app, and warmed once a session exists so a
// reader past that point never pays the fetch on a click — splitting alone
// moves the cost from first paint onto the first navigation to each screen,
// which is the one moment a record open is being measured. Wrapping the factory
// is what keeps the two lists one list: a screen cannot be added to the router
// and forgotten here.
const ROUTE_CHUNKS: Array<() => Promise<unknown>> = [];

// Registers a screen's chunk for warming and hands the factory back unchanged,
// so `lazy` keeps the loading and this keeps the list. A warmed chunk still
// SUSPENDS on its first render — `lazy` calls its factory and throws the promise
// whatever the module cache holds — so warming alone does not make a navigation
// cheap. What does is ScreenView deferring the route, which keeps React from
// committing the fallback it would then have to hold on screen.
function routed<T>(factory: () => Promise<T>): () => Promise<T> {
  ROUTE_CHUNKS.push(factory);
  return factory;
}

const AskAiScreen = lazy(
  routed(() =>
    import("./screens/ai").then((m) => ({ default: m.AskAiScreen })),
  ),
);
const BookingScreen = lazy(
  routed(() =>
    import("./screens/book").then((m) => ({ default: m.BookingScreen })),
  ),
);
const ClientSurfaceScreen = lazy(
  routed(() =>
    import("./screens/client").then((m) => ({
      default: m.ClientSurfaceScreen,
    })),
  ),
);
const DealScreen = lazy(
  routed(() =>
    import("./screens/deals").then((m) => ({ default: m.DealScreen })),
  ),
);
const DealsScreen = lazy(
  routed(() =>
    import("./screens/deals").then((m) => ({ default: m.DealsScreen })),
  ),
);
const DedupeScreen = lazy(
  routed(() =>
    import("./screens/dedupe").then((m) => ({ default: m.DedupeScreen })),
  ),
);
const FiltersScreen = lazy(
  routed(() =>
    import("./screens/filters").then((m) => ({ default: m.FiltersScreen })),
  ),
);
const HomeScreen = lazy(
  routed(() =>
    import("./screens/home").then((m) => ({ default: m.HomeScreen })),
  ),
);
const LeadScreen = lazy(
  routed(() =>
    import("./screens/leads").then((m) => ({ default: m.LeadScreen })),
  ),
);
const LeadsScreen = lazy(
  routed(() =>
    import("./screens/leads").then((m) => ({ default: m.LeadsScreen })),
  ),
);
const OAuthConsent = lazy(
  routed(() =>
    import("./screens/oauthconsent").then((m) => ({ default: m.OAuthConsent })),
  ),
);
const OfferScreen = lazy(
  routed(() =>
    import("./screens/offers").then((m) => ({ default: m.OfferScreen })),
  ),
);
const CompaniesScreen = lazy(
  routed(() =>
    import("./screens/organizations").then((m) => ({
      default: m.CompaniesScreen,
    })),
  ),
);
const CompanyScreen = lazy(
  routed(() =>
    import("./screens/organizations").then((m) => ({
      default: m.CompanyScreen,
    })),
  ),
);
const PartnersScreen = lazy(
  routed(() =>
    import("./screens/partners").then((m) => ({ default: m.PartnersScreen })),
  ),
);
const ContactsScreen = lazy(
  routed(() =>
    import("./screens/people").then((m) => ({ default: m.ContactsScreen })),
  ),
);
const PersonPageV2 = lazy(
  routed(() =>
    import("./screens/personpage").then((m) => ({ default: m.PersonPageV2 })),
  ),
);
const PreferenceCenterScreen = lazy(
  routed(() =>
    import("./screens/preferences").then((m) => ({
      default: m.PreferenceCenterScreen,
    })),
  ),
);
const ReportsScreen = lazy(
  routed(() =>
    import("./screens/reports").then((m) => ({ default: m.ReportsScreen })),
  ),
);
const SearchScreen = lazy(
  routed(() =>
    import("./screens/search").then((m) => ({ default: m.SearchScreen })),
  ),
);
const SettingsScreen = lazy(
  routed(() =>
    import("./screens/settings").then((m) => ({ default: m.SettingsScreen })),
  ),
);
const ShareScreen = lazy(
  routed(() =>
    import("./screens/share").then((m) => ({ default: m.ShareScreen })),
  ),
);
const TasksScreen = lazy(
  routed(() =>
    import("./screens/tasks").then((m) => ({ default: m.TasksScreen })),
  ),
);

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

// What stands in the content column when a screen itself cannot: its code is
// still arriving, the address names no page, or the address is half written. One
// container and one primitive for all three — they stand in the same place, so
// they take the same column every screen does, and only the sentence differs.
function ScreenNotice({ messageKey }: Readonly<{ messageKey: MessageKey }>) {
  const t = useT();
  return (
    <div className="wrap">
      <EmptyState>{t(messageKey)}</EmptyState>
    </div>
  );
}

// Split out of the dispatch table purely to keep the deals list/detail split in
// one place — it has its own "new" vs existing-id branch below the id check.
function DealsRoute({ id }: Readonly<{ id?: string }>) {
  return id && id !== "new" ? (
    <DealScreen id={id} />
  ) : (
    <DealsScreen startCreating={id === "new"} />
  );
}

// #/share/<record_type>/<record_id> (AS-3/4/5) — both segments are required;
// a bare #/share renders the honest pending state instead of a screen with
// nothing to share.
function ShareRoute({ id, id2 }: Readonly<{ id?: string; id2?: string }>) {
  return id && id2 ? (
    <ShareScreen recordType={id} recordId={id2} />
  ) : (
    <ScreenNotice messageKey="screen.pending" />
  );
}

// The reset form itself, so the two places that answer #/reset-password render
// ONE form: `App` below, ahead of the session gate and inside the pre-session
// frame, and the routed table, which is total over `Screen` and so has an entry
// for every address including this one.
//
// A stale or bare reset link has no token, so the embedded form renders as an
// ordinary login rather than ResetForm — and its own "restore the originally
// requested route" check (LoginForm's onSuccess) never fires home for a
// non-empty hash, which this one always is. Without an explicit navigate here, a
// successful sign-in from this route would leave the reader signed in but still
// looking at a login form.
function ResetRoute() {
  return <AuthScreen onAuthed={() => navigate({ screen: "home" })} />;
}

// #/ext/<unit> (ADR-0069) — the composed extension tier's one route into the
// SPA. The registry is generated per installation, so this arm is the SAME
// code in the vanilla tree, where every unit name misses and the honest
// not-found card renders; that lane is the default one and must never crash or
// paint a blank frame.
//
// A unit surface comes from one of two places, in this order:
//
//   1. The unit's OWN screen: extensions/<name>/frontend/ is a pnpm workspace
//      package (@margince-ext/<name>) whose default export is a component, and
//      gen-composition lists it in the composed screen registry
//      ("@composition/screens"). notes, the reference extension, is the one
//      such screen today. This is unit-authored TSX compiled into the SPA
//      bundle, which is why collectUnitFrontend refuses a package that is not
//      private, mis-named, or takes a direct dependency on a hosted framework,
//      and why check-ext-imports.sh gates what such a screen may import.
//   2. Otherwise the contract-derived descriptor set — the operations the
//      unit's fragments published, which is all the app can honestly say about
//      a unit nobody wrote a screen for (de, yogi, crm-hello).
//
// The registry is consulted only AFTER the descriptor resolves, so a screen
// cannot render for a unit this installation did not compose: an entry for a
// disabled unit is inert rather than a route into a surface with no server
// behind it.
// The generated registry is emitted UNTYPED, and the annotation lands here.
//
// That file is written to two locations at different depths — the vanilla stub
// under src/composition/ and the composed output under build/composition/ —
// and stubMatchesVanilla requires the two to be byte-identical, so it can carry
// neither a relative type import (the paths would differ) nor a bare one
// (nothing resolves from build/composition/). Annotating at the import site
// costs nothing and moves the check rather than losing it: a unit whose default
// export is not a component fails HERE, in core, in the composed lane, at the
// one place both halves of the registry are visible at once.
const extensionScreens: ExtensionScreenRegistry = composedScreens;

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
  // Object.hasOwn, not a bare index: the generated registry is an object
  // literal, so `extensionScreens["constructor"]` answers from the prototype
  // chain with Object itself — truthy, and a function, so the dispatch below
  // mounts it and React dies on "Objects are not valid as a React child". The
  // unit-name grammar admits `constructor`, and the fallback card is supposed
  // to be what a unit without a screen gets.
  const UnitScreen = Object.hasOwn(extensionScreens, unit.name)
    ? extensionScreens[unit.name]
    : undefined;
  if (UnitScreen) {
    return <UnitScreen />;
  }
  return (
    <div className="wrap narrow">
      {/* level 1: the head yields to a composed unit, so this card is the only
          thing left that can name the page. A unit with no screen would
          otherwise have no page-level heading at all. */}
      <SectionHeader title={unit.name} sub={t("ext.operations")} level={1} />
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

// The path segments below the screen. Only two of the four are dispatched on:
// id3 belongs to the screens that read it themselves (the consent return's
// provider), and nothing routes on it.
type ScreenArgs = Readonly<{ id?: string; id2?: string }>;

// One entry per address, and `Record<Screen, …>` is what makes that set
// COMPLETE: a screen added to the union in app/router.tsx with no view here
// fails the build instead of quietly rendering the page a hash nobody typed
// gets. A fallback arm cannot tell an unwired screen from an unknown address.
const SCREEN_VIEWS: Readonly<Record<Screen, (args: ScreenArgs) => ReactNode>> =
  {
    home: () => <HomeScreen />,
    // The tab rides the URL, so it survives a reload and can be linked to.
    // An unknown segment falls back to overview rather than rendering an
    // empty page: a mistyped link should land somewhere, not nowhere.
    contacts: ({ id, id2 }) =>
      id ? (
        <PersonPageV2 id={id} tab={isPersonTab(id2) ? id2 : "overview"} />
      ) : (
        <ContactsScreen />
      ),
    companies: ({ id }) =>
      id ? <CompanyScreen id={id} /> : <CompaniesScreen />,
    partners: () => <PartnersScreen />,
    leads: ({ id }) => (id ? <LeadScreen id={id} /> : <LeadsScreen />),
    deals: ({ id }) => <DealsRoute id={id} />,
    tasks: () => <TasksScreen />,
    inbox: () => <InboxScreen />,
    reports: () => <ReportsScreen />,
    ai: () => <AskAiScreen />,
    settings: ({ id }) => <SettingsScreen tab={id} />,
    dedupe: () => <DedupeScreen />,
    // The object rides the URL so a filter surface can be linked to; an
    // unknown segment falls back to contacts inside the screen rather than
    // rendering a page with no vocabulary to offer.
    filters: ({ id }) => <FiltersScreen id={id} />,
    offers: ({ id }) =>
      id ? (
        <OfferScreen id={id} />
      ) : (
        <ScreenNotice messageKey="screen.pending" />
      ),
    search: ({ id }) => <SearchScreen q={id ? safeDecode(id) : ""} />,
    share: ({ id, id2 }) => <ShareRoute id={id} id2={id2} />,
    onboarding: () => <OnboardingScreen />,
    client: () => <ClientSurfaceScreen />,
    // #/book/<host_slug> is the anonymous public variant
    book: ({ id }) => <BookingScreen hostSlug={id} />,
    // #/preferences/<token> — anonymous; the token in the path is the
    // whole capability (security: [] in the contract).
    preferences: ({ id }) => <PreferenceCenterScreen token={id} />,
    // reached only via the server's redirect off GET /oauth/authorize
    // (#/oauth-consent?…&consent=<nonce>) — never a rail destination.
    "oauth-consent": () => <OAuthConsent />,
    [RESET_ROUTE]: () => <ResetRoute />,
    [EXTENSION_SCREEN]: ({ id }) => <ExtensionRoute name={id} />,
    // A hash naming no address this app answers. The shell's heading says the
    // same word (shell.unknownPage), so the reader is told once by the page and
    // once by the column, and neither claims a page by that name exists.
    "not-found": () => <ScreenNotice messageKey="shell.unknownPage" />,
  };

function ScreenView({
  screen,
  id,
  id2,
}: Readonly<{ screen: Screen; id?: string; id2?: string }>) {
  // The route is DEFERRED, and that is what keeps splitting from costing the
  // reader anything on a navigation. React renders the asked-for screen in the
  // background; when it suspends it keeps the screen already on the page instead
  // of committing the fallback — which it would then hold on screen for its own
  // anti-flicker interval before revealing the content. Measured on a record
  // open: 64ms unsplit, 813ms split with a committed fallback, 44ms split and
  // deferred, with the same requests on the wire every time.
  //
  // Deferred as one value, never three: a screen that updated while an id lagged
  // would render a company page against a person's id.
  const asked = useMemo(() => ({ screen, id, id2 }), [screen, id, id2]);
  const shown = useDeferredValue(asked);
  // The boundary a lazy screen suspends against on the FIRST paint, when there
  // is no previous screen to hold. A failure to FETCH one — the chunk 404s
  // because a deploy replaced it under a tab that was already open — is thrown
  // from here into AppErrorBoundary (app/errorboundary.tsx), which is what turns
  // it into the retry card instead of a blank frame.
  return (
    <Suspense fallback={<ScreenNotice messageKey="common.loading" />}>
      {SCREEN_VIEWS[shown.screen]({ id: shown.id, id2: shown.id2 })}
    </Suspense>
  );
}

// The anonymous public surfaces render without a session — their slug in the
// path is the whole address (security: [] in the contract).
const PUBLIC_SCREENS: ReadonlySet<Screen> = new Set(["book", "preferences"]);

// Screens the onboarding gate must never navigate away from, beyond
// onboarding itself. The OAuth consent screen carries a single-use,
// cookie-bound nonce in the hash (armed by GET /oauth/authorize's 302); the
// gate's navigate() rewrites location.hash, which would destroy that nonce
// with nothing able to recover it — unlike an ordinary screen, there is no
// route back once this one is skipped mid-flight. This is a narrow carve-out
// for a request in flight, not a relaxation of the gate for the screen in
// general.
const ONBOARDING_GATE_EXEMPT_SCREENS: ReadonlySet<Screen> = new Set([
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
        <ResetRoute />
      </RaillessFrame>
    );
  }
  return <AuthedApp route={route} />;
}

// UnavailableOrClaimable splits the 503 the boundary already reached into its
// two product states. "Not ready" is true of both, but only one of them has
// something the person in front of the browser can do: an installation that
// holds no organization and is WAITING to be claimed (ADR-0105) gets the claim
// screen; anything else keeps the availability message.
//
// The probe runs only on this branch, and only for the installation kind: a
// connectivity failure says nothing about whether a claim is possible, and
// asking would replace a true message with a guess. While it is in flight the
// availability screen stands — it is the honest answer until the probe says
// otherwise, and it is what the user would have seen anyway.
function UnavailableOrClaimable({
  kind,
  onRetry,
}: Readonly<{ kind: "connection" | "installation"; onRetry: () => void }>) {
  const claimable = useQuery({
    queryKey: ["setup-status"],
    queryFn: fetchSetupStatus,
    enabled: kind === "installation",
    retry: false,
  });
  // Retry re-probes BOTH. fetchSetupStatus resolves rather than throws, so a
  // one-off failure caches a `false` under this key — without refetching it,
  // "Try again" would keep showing the availability screen for an installation
  // that has been claimable all along, and only a page reload would recover.
  const retryBoth = () => {
    onRetry();
    if (kind === "installation") {
      void claimable.refetch();
    }
  };
  if (kind === "installation" && claimable.data?.claimable) {
    // A successful claim provisions the installation, so the same /v1/me probe
    // that sent us here now answers — the boundary re-resolves rather than this
    // screen deciding where to go next.
    return <SetupClaimScreen onClaimed={onRetry} />;
  }
  return <AvailabilityScreen kind={kind} onRetry={retryBoth} />;
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
  const taskbarPreview = uiPreviewTaskbarEnabled();
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
    // The account is authenticated; what it lacks is a password of its own.
    // Sending it to the login screen would loop, because the credentials are
    // correct and using them again lands in the same refusal — so the boundary
    // renders the one thing that can resolve it.
    if (kind === "must-change-password") {
      return (
        <RaillessFrame>
          <ForcedPasswordChangeScreen onChanged={() => me.refetch()} />
        </RaillessFrame>
      );
    }
    if (kind !== "unauthorized") {
      return (
        <RaillessFrame>
          <UnavailableOrClaimable kind={kind} onRetry={() => me.refetch()} />
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
      {/* One agent affordance in this corner, never two: the taskbar preview's
          page half is the same offer the FAB makes, so the switch that draws the
          bar takes the FAB down (app/ui-preview.ts). */}
      {taskbarPreview ? (
        <AgentTaskbar route={route} />
      ) : (
        <AskFab route={route} />
      )}
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
// Fetches the route chunks in the background, once, for a reader who is past
// the login screen. It runs at idle so it never competes with the screen the
// reader is actually looking at, and with a deadline so a busy tab cannot defer
// it indefinitely — a click that arrives before the warm-up finishes suspends
// exactly as it would have without one, so the worst case is the behaviour
// without this hook rather than a slower one.
function useWarmRouteChunks() {
  useEffect(() => {
    let cancelled = false;
    // ONE chunk in flight at a time. Asking for all of them at once empties the
    // browser's per-host connection pool, and the read the reader is actually
    // waiting on — the record they just opened — queues behind two dozen files
    // nobody asked for. Warming that makes the next navigation slower is worse
    // than not warming at all.
    const warm = async () => {
      for (const load of ROUTE_CHUNKS) {
        if (cancelled) {
          return;
        }
        try {
          await load();
        } catch (reason: unknown) {
          // Nobody asked for this screen yet, so a failed fetch is not the
          // reader's problem: the navigation that does ask will suspend and
          // surface it through the error boundary. It is still worth saying out
          // loud, because a chunk that cannot be fetched is a broken deploy.
          console.warn("route chunk warm-up failed", reason);
        }
      }
    };
    // Typed as always present, and absent in jsdom and in older Safari, so the
    // check is a runtime one whatever the DOM lib claims.
    if (typeof window.requestIdleCallback === "function") {
      const handle = window.requestIdleCallback(() => void warm(), {
        timeout: 500,
      });
      return () => {
        cancelled = true;
        window.cancelIdleCallback(handle);
      };
    }
    const handle = window.setTimeout(() => void warm(), 0);
    return () => {
      cancelled = true;
      window.clearTimeout(handle);
    };
  }, []);
}

function AuthedShell({
  children,
  onOpenSearch,
}: Readonly<{ children: ReactNode; onOpenSearch: () => void }>) {
  useWarmRouteChunks();
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
  // A plain column. It used to borrow onboarding's `.ob-top`, which is that
  // flow's sticky blurred header — so the splash drew a bar with a bottom
  // border across a screen that has no header, and took that class's
  // horizontal padding on top of the column's own.
  return (
    <div className="wrap narrow">
      <EmptyState>{t("auth.checking")}</EmptyState>
    </div>
  );
}
