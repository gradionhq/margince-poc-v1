import {
  Check,
  ChevronRight,
  ChevronsUpDown,
  LogOut,
  UserRound,
} from "lucide-react";
import {
  type KeyboardEvent,
  type RefObject,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import type { components } from "../api/schema";
import { Avatar, SegmentedControl } from "../design-system/atoms";
import { useT } from "../i18n";
import { useLogout, useMe } from "../screens/common";
import { SETTINGS_SCREEN } from "../screens/settings";
import { usePopoverDismiss } from "./popover";
import { routeHash } from "./router";
import {
  setThemeChoice,
  THEME_CHOICES,
  type ThemeChoice,
  useThemeChoice,
} from "./theme";
import "./account.css";

type SessionUser = components["schemas"]["MeResponse"]["user"];

/**
 * The product's ONE settings door.
 *
 * The sidebar's foot used to carry a second one beside this menu; it is gone,
 * and everything that reached the settings level through it reaches it here.
 * Derived from the router rather than spelled, so the door cannot come to point
 * at a hash the router does not parse.
 */
const SETTINGS_HREF = routeHash({ screen: SETTINGS_SCREEN });

/** The appearance choices, in the order the submenu shows them, named. */
const THEME_LABEL_KEYS = {
  light: "theme.light",
  dark: "theme.dark",
  system: "theme.system",
} as const satisfies Record<ThemeChoice, string>;

/**
 * Where each row of the menu sits in the roving walk.
 *
 * Named rather than counted at the call sites, because the theme row's index is
 * read from three places — the tabstop, the submenu's way back, and the walk
 * itself — and three hand-written `1`s is how one of them comes to disagree.
 */
const SETTINGS_SEAT = 0;
const THEME_SEAT = 1;
const SIGN_OUT_SEAT = 2;
const SEAT_COUNT = 3;

/**
 * Who is signed in, in the shapes the block prints them in.
 *
 * Everything here is derived from what the session actually carries: a person
 * without a display name is their address, and nobody is ever given a name the
 * product made up to fill the line.
 */
function identityOf(user: SessionUser | undefined) {
  const name = user?.display_name ?? "";
  const email = user?.email ?? "";
  const label = name || email;
  return {
    name,
    email,
    label,
    // Initials, or a single letter from the address — never a fabricated name.
    initials:
      label
        .split(/[\s@._-]+/)
        .filter(Boolean)
        .slice(0, 2)
        .map((part) => part[0]?.toUpperCase() ?? "")
        .join("") || undefined,
    // Both lines in one sentence, for the state that has no room to print them.
    spoken: name && email ? `${name} — ${email}` : label,
  };
}

type Identity = ReturnType<typeof identityOf>;

/**
 * The person, printed: their display name when the record carries one, otherwise
 * the address alone — which is then not repeated underneath itself.
 *
 * One spelling, because the trigger, the panel and the phone sheet all print it
 * and a second copy is how they would come to disagree about which line is which.
 */
function IdentityLines({ identity }: Readonly<{ identity: Identity }>) {
  return (
    <span className="acctwho">
      <b>{identity.label}</b>
      {identity.name && identity.email && (
        <span className="acctmail">{identity.email}</span>
      )}
    </span>
  );
}

/**
 * What a row needs in order to BE a menu item: the role, its place in the roving
 * tabstop, the handle the menu moves focus with, and the callback that records
 * where focus landed when the reader put it there themselves.
 *
 * Optional at every call site, because the phone sheet's rows are not a menu.
 * They are ordinary content in a sheet, walked with Tab, and announcing
 * `role="menuitem"` outside a `role="menu"` would promise a keyboard contract
 * nothing there implements.
 */
type RowSeat = Readonly<{
  ref: (element: HTMLElement | null) => void;
  tabIndex: number;
  onFocus: () => void;
}>;

/**
 * The way into settings — the only one the product has.
 *
 * `onActivate` is how a caller says this click should BOTH act and close: the
 * menu is chrome over the page the link is about to replace, and a popover left
 * standing over the destination it just opened is the reader's to dismiss for no
 * reason. The phone sheet passes nothing; its rows are the surface.
 */
function SettingsRow({
  seat,
  onActivate,
}: Readonly<{ seat?: RowSeat; onActivate?: () => void }>) {
  const t = useT();
  return (
    <a
      className="acctrow"
      href={SETTINGS_HREF}
      role={seat ? "menuitem" : undefined}
      tabIndex={seat?.tabIndex}
      ref={seat?.ref}
      onFocus={seat?.onFocus}
      onClick={onActivate}
    >
      {t("nav.settings")}
    </a>
  );
}

/**
 * The way out, with the guard against a second POST while the first is in
 * flight. One spelling for the menu and the sheet: two would be two ways to sign
 * out, and only one of them would keep the guard.
 */
function SignOutRow({ seat }: Readonly<{ seat?: RowSeat }>) {
  const t = useT();
  const logout = useLogout();
  return (
    <button
      type="button"
      className="acctrow"
      role={seat ? "menuitem" : undefined}
      tabIndex={seat?.tabIndex}
      ref={seat?.ref}
      onFocus={seat?.onFocus}
      disabled={logout.isPending}
      onClick={() => logout.mutate()}
    >
      <LogOut size={15} aria-hidden />
      {t("shell.signOutAria")}
    </button>
  );
}

/** A disabled control cannot hold focus, so the roving walk steps over it rather
 *  than landing the reader on a row nothing happens on. */
function isSeatFocusable(element: HTMLElement | null): element is HTMLElement {
  return (
    element !== null &&
    !(element instanceof HTMLButtonElement && element.disabled)
  );
}

/**
 * The next seat a reader can actually stand on, wrapping at both ends.
 *
 * `count * count` keeps the modulus positive for a backwards walk; the loop
 * gives up after one full lap, which is the case where every other row is
 * disabled and the reader stays where they are.
 */
function nextSeat(
  seats: readonly (HTMLElement | null)[],
  from: number,
  delta: number,
): number {
  const count = seats.length;
  for (let hop = 1; hop <= count; hop++) {
    const at = (from + delta * hop + count * count) % count;
    if (isSeatFocusable(seats[at])) {
      return at;
    }
  }
  return from;
}

/**
 * Light / Dark / System, as a radio group that happens to be a menu.
 *
 * `menuitemradio` rather than `menuitem`, because these three are one answer to
 * one question and `aria-checked` is the only thing that says which answer is
 * standing — the tick is its visible half, and a tick with no `aria-checked`
 * behind it is a decoration.
 *
 * Picking one does NOT close the menu. This is the appearance control, and the
 * whole of its feedback is the document repainting under the open panel with the
 * tick moving to the row that did it; closing on pick would take that away and
 * make the reader re-open the menu to see what they chose.
 *
 * Escape is deliberately absent here. `usePopoverDismiss` (app/popover.ts) is
 * what dismisses this layer, and the parent menu's copy of the same hook stands
 * down while the row that opened this one still reads `aria-expanded="true"`.
 * One keystroke, one layer, one implementation.
 */
function ThemeSubmenu({
  panel,
  onBack,
}: Readonly<{
  panel: RefObject<HTMLDivElement | null>;
  onBack: () => void;
}>) {
  const t = useT();
  const choice = useThemeChoice();
  const items = useRef<(HTMLButtonElement | null)[]>([]);
  const [active, setActive] = useState(() => {
    const at = THEME_CHOICES.indexOf(choice);
    return at < 0 ? 0 : at;
  });
  // Where the reader starts, captured at mount: the row that is already checked,
  // so opening the chooser puts them on the appearance they currently have.
  const opensAt = useRef(active);

  useEffect(() => {
    items.current[opensAt.current]?.focus();
  }, []);

  const move = (at: number) => {
    setActive(at);
    items.current[at]?.focus();
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const last = THEME_CHOICES.length - 1;
    switch (event.key) {
      case "ArrowDown":
        move(active === last ? 0 : active + 1);
        break;
      case "ArrowUp":
        move(active === 0 ? last : active - 1);
        break;
      case "Home":
        move(0);
        break;
      case "End":
        move(last);
        break;
      case "ArrowLeft":
        onBack();
        break;
      default:
        return;
    }
    // Handled HERE and nowhere else: the parent menu listens on the same bubble
    // path, and without this its own roving tabstop would move under a reader
    // who is standing in this one.
    event.preventDefault();
    event.stopPropagation();
  };

  return (
    <div
      className="accountsub"
      role="menu"
      aria-label={t("shell.theme")}
      ref={panel}
      onKeyDown={onKeyDown}
    >
      {THEME_CHOICES.map((option, index) => (
        <button
          key={option}
          type="button"
          className="acctrow"
          role="menuitemradio"
          aria-checked={option === choice}
          tabIndex={index === active ? 0 : -1}
          ref={(element) => {
            items.current[index] = element;
          }}
          onFocus={() => setActive(index)}
          onClick={() => setThemeChoice(option)}
        >
          {/* The tick keeps its column whether or not it is drawn, so the three
              labels stand on one left edge instead of shifting as the choice
              moves. */}
          <span className="accttick" aria-hidden>
            {option === choice && <Check size={14} />}
          </span>
          {t(THEME_LABEL_KEYS[option])}
        </button>
      ))}
    </div>
  );
}

/**
 * What the menu holds: who you are, the settings door, the appearance choice,
 * and the way out.
 *
 * A real `role="menu"`, which the two-row panel this replaces did not need and
 * this one does — a submenu a reader can only reach with a pointer is a setting
 * only pointer users can change. So: a roving tabstop, Up/Down between the rows,
 * Home/End to the ends, Right or Enter into the submenu, Left back out.
 *
 * The identity block is not a menu item and carries no role. It is a statement
 * about who is signed in, and it is also the menu's own accessible name, so a
 * reader entering the panel in menu mode hears whose account they are about to
 * act on rather than having to leave the menu to find out.
 */
function AccountPanel({
  identity,
  id,
  panel,
  onDismiss,
}: Readonly<{
  identity: Identity;
  id: string;
  panel: RefObject<HTMLDivElement | null>;
  onDismiss: () => void;
}>) {
  const t = useT();
  const rows = useRef<(HTMLElement | null)[]>([]);
  const [active, setActive] = useState(SETTINGS_SEAT);
  const [themeOpen, setThemeOpen] = useState(false);
  const submenu = useRef<HTMLDivElement>(null);

  // The menu takes focus when it opens. A panel that leaves focus on the trigger
  // is one the keyboard reader has to walk into with a key nothing told them
  // about, and it is what makes the Escape return below mean anything.
  useEffect(() => {
    rows.current[SETTINGS_SEAT]?.focus();
  }, []);

  /**
   * Close the submenu, and put focus back on the row that opened it.
   *
   * Only when the submenu actually HELD focus — the same rule the menu's own
   * dismissal keeps: an outside click usually lands on something focusable of
   * its own, and pulling focus onto the theme row afterwards would undo what the
   * click just did.
   */
  const closeTheme = useCallback(() => {
    const held = submenu.current?.contains(document.activeElement) ?? false;
    setThemeOpen(false);
    if (held) {
      setActive(THEME_SEAT);
      rows.current[THEME_SEAT]?.focus();
    }
  }, []);

  // The INNER dismissal layer, from the same hook as the outer one. Its listener
  // is registered after the menu's — this panel only mounts inside an open menu,
  // and the submenu only opens later — so on Escape the menu's copy runs first,
  // sees the still-expanded theme row, and stands down for this one.
  usePopoverDismiss(themeOpen, submenu, closeTheme);

  const move = (at: number) => {
    setActive(at);
    rows.current[at]?.focus();
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    switch (event.key) {
      case "ArrowDown":
        move(nextSeat(rows.current, active, 1));
        break;
      case "ArrowUp":
        move(nextSeat(rows.current, active, -1));
        break;
      case "Home":
        move(nextSeat(rows.current, SEAT_COUNT - 1, 1));
        break;
      case "End":
        move(nextSeat(rows.current, 0, -1));
        break;
      default:
        return;
    }
    event.preventDefault();
  };

  const seat = (index: number): RowSeat => ({
    ref: (element) => {
      rows.current[index] = element;
    },
    tabIndex: active === index ? 0 : -1,
    onFocus: () => setActive(index),
  });

  return (
    <div
      className="accountmenu"
      id={id}
      role="menu"
      aria-label={
        identity.spoken
          ? `${t("shell.accountAria")} — ${identity.spoken}`
          : t("shell.accountAria")
      }
      ref={panel}
      onKeyDown={onKeyDown}
    >
      {/* Nothing at all rather than an empty line while /me is in flight or
          carries neither a name nor an address. */}
      {identity.label && (
        <>
          <div className="acctmenuwho">
            <IdentityLines identity={identity} />
          </div>
          <hr />
        </>
      )}
      <SettingsRow seat={seat(SETTINGS_SEAT)} onActivate={onDismiss} />
      {/* `role="none"` on the wrapper: a menu's children are its items, and a
          plain div between the two would break that parentage. The wrapper earns
          its place by being what the flyout is positioned against. */}
      <div className="acctthemeseat" role="none">
        <button
          type="button"
          className="acctrow"
          role="menuitem"
          aria-haspopup="menu"
          aria-expanded={themeOpen}
          tabIndex={active === THEME_SEAT ? 0 : -1}
          ref={(element) => {
            rows.current[THEME_SEAT] = element;
          }}
          onFocus={() => setActive(THEME_SEAT)}
          onClick={() => setThemeOpen((was) => !was)}
          onKeyDown={(event) => {
            if (event.key !== "ArrowRight") {
              return;
            }
            setThemeOpen(true);
            event.preventDefault();
            event.stopPropagation();
          }}
        >
          <span className="acctrowlabel">{t("shell.theme")}</span>
          <ChevronRight size={14} className="acctmore" aria-hidden />
        </button>
        {themeOpen && <ThemeSubmenu panel={submenu} onBack={closeTheme} />}
      </div>
      {/* The groups: where you go, then the way out. An <hr> rather than a border
          on the last row — it separates a group, and a screen reader is told so. */}
      <hr />
      <SignOutRow seat={seat(SIGN_OUT_SEAT)} />
    </div>
  );
}

/**
 * The same destinations, flat — what the phone sheet gets.
 *
 * At phone width the rail is a bottom bar and "More" expands it into a sheet
 * over the page. A popover anchored inside that sheet has nowhere to open, so
 * the sheet takes the rows themselves instead of the trigger that hides them.
 * The identity line comes with them: the sheet is the phone's whole sidebar, and
 * without the trigger nothing else on it says who is signed in.
 */
export function AccountRows() {
  const t = useT();
  const me = useMe();
  const choice = useThemeChoice();
  const identity = identityOf(me.data?.user);
  return (
    <div className="accountrows">
      {identity.label && (
        <div className="acctsheetwho">
          <IdentityLines identity={identity} />
        </div>
      )}
      <SettingsRow />
      {/* Flat, not a flyout. A submenu is a hover-and-aim shape: it costs a
          second tap to open, a third to pick, and it opens over the list it came
          from on a surface that is already the width of the screen. Three
          options all visible at once is one tap, and it is the control the
          design system already has for a closed set. */}
      <div className="acctthemeflat">
        <span className="acctthemelabel">{t("shell.theme")}</span>
        <SegmentedControl
          options={THEME_CHOICES}
          value={choice}
          onChange={setThemeChoice}
          labels={{
            light: t(THEME_LABEL_KEYS.light),
            dark: t(THEME_LABEL_KEYS.dark),
            system: t(THEME_LABEL_KEYS.system),
          }}
          label={t("shell.theme")}
        />
      </div>
      <hr />
      <SignOutRow />
    </div>
  );
}

/**
 * What the trigger says beside the avatar.
 *
 * Where the trigger is the avatar and nothing else — the top bar always, the
 * collapsed rail at 64px — the two lines do not fit, so the sentence they carry
 * is present for a screen reader and clipped for the eye. That is the technique
 * the collapsed agent panel uses, rather than a tooltip standing in for text
 * that was never rendered.
 */
function TriggerLines({
  identity,
  avatarOnly,
  spokenId,
}: Readonly<{
  identity: Identity;
  avatarOnly: boolean;
  spokenId: string;
}>) {
  if (avatarOnly) {
    return identity.spoken ? (
      <span className="sr-only" id={spokenId}>
        {identity.spoken}
      </span>
    ) : null;
  }
  return (
    <>
      <IdentityLines identity={identity} />
      <ChevronsUpDown size={15} className="acctchev" aria-hidden />
    </>
  );
}

/** Which chrome the block wears. `rail` is the sidebar's row — name over address
 *  — and `topbar` is the avatar at the top-right of the viewport. */
export type AccountVariant = "rail" | "topbar";

/**
 * The account block: who is signed in, and the things it is FOR.
 *
 * It is now the product's ONE appearance control and its ONE door into settings
 * — the sidebar's foot carried that door and no longer exists — so the panel
 * holds the identity at the top, the settings door, the theme choice, and the
 * way out. Theme is here rather than only on a settings page because a reader
 * changing the appearance wants to see the appearance change, not to navigate to
 * a form and find their way back.
 *
 * `variant` picks the chrome; `collapsed` is the rail's own width state and says
 * nothing about the top bar, where the trigger is an avatar at every width.
 */
export function AccountMenu({
  variant = "rail",
  collapsed = false,
}: Readonly<{ variant?: AccountVariant; collapsed?: boolean }>) {
  const t = useT();
  const me = useMe();
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const spokenId = useId();
  const menuId = useId();

  /**
   * Close, and put focus back where it can be used.
   *
   * Dismissing the menu unmounts whatever row was focused, and an unmounted
   * focus owner leaves the document focused on `<body>` — from there a keyboard
   * user's next Tab starts at the top of the page, having lost the chrome they
   * were standing in. The account trigger is where they came from, so it is
   * where they go back to.
   *
   * Only when the menu actually HELD focus, which is the difference between
   * restoring focus and stealing it: an outside click usually lands on something
   * focusable of its own, and pulling focus onto the trigger after it would undo
   * what the click just did.
   */
  const dismiss = useCallback(() => {
    const held = menu.current?.contains(document.activeElement) ?? false;
    setOpen(false);
    if (held) {
      trigger.current?.focus();
    }
  }, []);

  const identity = identityOf(me.data?.user);
  // The top bar has no room for the two lines at any width; the rail has none
  // only while it is collapsed.
  const avatarOnly = variant === "topbar" || collapsed;

  // One dismissal for every popover in the chrome (app/popover.ts): Escape from
  // anywhere inside, any outside click, and the opening click deferred past.
  usePopoverDismiss(open, menu, dismiss);

  return (
    <div
      // Namespaced, because the variant's own word is a class the CHROME already
      // owns: `account topbar` matched `.topbar` and the block inherited the
      // strip's grid, padding and bottom rule — an avatar in a 120px box with a
      // hairline under it.
      className={["account", `acct-${variant}`, collapsed ? "collapsed" : ""]
        .filter(Boolean)
        .join(" ")}
    >
      <button
        type="button"
        className="user"
        ref={trigger}
        // Where the row PRINTS the person's name, WCAG 2.5.3 (Label in Name)
        // requires that visible text to be part of the accessible name —
        // otherwise someone driving the app by voice says the only word they can
        // see and reaches nothing. Avatar-only there is no visible text to
        // match, so the control is named by what it does.
        aria-label={
          avatarOnly || !identity.label
            ? t("shell.accountAria")
            : `${identity.label} — ${t("shell.accountAria")}`
        }
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        // With no room to print who is signed in, the sentence is carried for a
        // screen reader and clipped for the eye. It is wired as the DESCRIPTION
        // rather than left to be read in flow, because `aria-label` above
        // replaces the button's contents for name computation and would
        // otherwise silence it. The `title` is the pointer's version of the same
        // sentence, and never the accessible name.
        aria-describedby={avatarOnly && identity.spoken ? spokenId : undefined}
        title={avatarOnly && identity.spoken ? identity.spoken : undefined}
        onClick={() => setOpen((current) => !current)}
      >
        {identity.label ? (
          // The one chip, from the design system. The tint is keyed on the
          // address rather than the display name, so it survives a rename and
          // matches every other chip drawn for the same person.
          <Avatar
            identity={identity.email || undefined}
            name={identity.label}
          />
        ) : (
          // The session has not resolved yet. The chip keeps its box so the row
          // does not jump when the name arrives, and shows a person glyph rather
          // than an empty circle or initials of a name nobody has.
          <span className="avatar avatar-sm" aria-hidden>
            <UserRound size={15} />
          </span>
        )}
        <TriggerLines
          identity={identity}
          avatarOnly={avatarOnly}
          spokenId={spokenId}
        />
      </button>
      {open && (
        <AccountPanel
          identity={identity}
          id={menuId}
          panel={menu}
          onDismiss={dismiss}
        />
      )}
    </div>
  );
}
