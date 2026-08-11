import {
  ChevronsUpDown,
  LogOut,
  Moon,
  Settings,
  Sun,
  UserRound,
} from "lucide-react";
import { useCallback, useId, useRef, useState } from "react";
import type { components } from "../api/schema";
import { useT } from "../i18n";
import { useLogout, useMe } from "../screens/common";
import { LocaleMenu } from "./localemenu";
import { usePopoverDismiss } from "./popover";
import { useTheme } from "./theme";
import "./account.css";

type SessionUser = components["schemas"]["MeResponse"]["user"];

// The two preference rows: language, then theme, each naming the setting and
// stating what it is set to. Both halves of the name are load-bearing — a menu
// item has to say what activating it does, or the name describes a state rather
// than a control, and WCAG 2.5.3 (Label in Name) requires the visible label to be
// part of the name, or a speech-input user who says the word they can read
// ("Language") activates nothing.
//
// Both keep the account menu OPEN: they stop the click from reaching the document
// listener that dismisses it. Changing a preference is a thing you may want to do
// twice, or undo immediately, and a menu that closed under you took the theme
// you just picked out of view along with the control that picked it. Settings and
// sign-out leave the menu on purpose — they leave the screen.
function MenuPreferences() {
  const t = useT();
  const [theme, toggleTheme] = useTheme();
  const light = theme === "light";
  return (
    <>
      {/* Language is a menu of its own, not a toggle — three locales ship, and a
          toggle cannot say where the next click lands. It brings its own list,
          its own keyboard movement and its own focus handling (localemenu.tsx);
          what it takes from here is the row's chrome. */}
      <LocaleMenu className="menurow" />
      <button
        type="button"
        aria-label={`${t("shell.theme")}: ${
          light ? t("theme.switchToDark") : t("theme.switchToLight")
        }`}
        onClick={(event) => {
          event.stopPropagation();
          toggleTheme();
        }}
      >
        {light ? <Sun size={15} aria-hidden /> : <Moon size={15} aria-hidden />}
        {t("shell.theme")}
        <span className="menuvalue">
          {light ? t("theme.light") : t("theme.dark")}
        </span>
      </button>
    </>
  );
}

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

/**
 * What the trigger says beside the avatar.
 *
 * Collapsed the rail is 64px wide and the two lines do not fit, so the sentence
 * they carry is present for a screen reader and clipped for the eye — the
 * technique the collapsed agent panel uses, rather than a tooltip standing in
 * for text that was never rendered.
 */
function TriggerLines({
  identity,
  collapsed,
  spokenId,
}: Readonly<{
  identity: ReturnType<typeof identityOf>;
  collapsed: boolean;
  spokenId: string;
}>) {
  if (collapsed) {
    return identity.spoken ? (
      <span className="sr-only" id={spokenId}>
        {identity.spoken}
      </span>
    ) : null;
  }
  return (
    <>
      {/* The primary line is whoever this person is: their display name when the
          record carries one, otherwise the address alone — which is then not
          repeated underneath itself. */}
      <span className="acctwho">
        <b>{identity.label}</b>
        {identity.name && identity.email && (
          <span className="acctmail">{identity.email}</span>
        )}
      </span>
      <ChevronsUpDown size={15} className="acctchev" aria-hidden />
    </>
  );
}

/**
 * The account block at the foot of the sidebar.
 *
 * It owns everything that belongs to this person: the settings surfaces, their
 * language and theme, and the way out. A menu rather than a row of chrome
 * buttons — the rail carries one account affordance, and sign-out beside every
 * screen invites a misclick.
 *
 * It sits at the foot because that is where the person is, under the
 * destinations that are the product; the menu therefore opens UPWARD, out of the
 * trigger and over the navigation rather than off the bottom of the viewport.
 */
export function AccountMenu({ collapsed }: Readonly<{ collapsed: boolean }>) {
  const t = useT();
  const me = useMe();
  const logout = useLogout();
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const spokenId = useId();

  /**
   * Close, and put focus back where it can be used.
   *
   * Dismissing the menu unmounts whatever row was focused, and an unmounted
   * focus owner leaves the document focused on `<body>` — from there a keyboard
   * user's next Tab starts at the top of the page, having lost the sidebar they
   * were standing in. The account trigger is where they came from, so it is where
   * they go back to.
   *
   * Only when the menu actually HELD focus, which is the difference between
   * restoring focus and stealing it: an outside click usually lands on something
   * focusable of its own (a field, a rail link), and pulling focus onto the
   * trigger after it would undo what the click just did.
   */
  const dismiss = useCallback(() => {
    const held = menu.current?.contains(document.activeElement) ?? false;
    setOpen(false);
    if (held) {
      trigger.current?.focus();
    }
  }, []);

  const identity = identityOf(me.data?.user);

  // One dismissal for every popover in the chrome (app/popover.ts): Escape from
  // anywhere inside, any outside click, and the opening click deferred past.
  usePopoverDismiss(open, menu, dismiss);

  return (
    <div className={collapsed ? "account collapsed" : "account"}>
      <button
        type="button"
        className="user"
        ref={trigger}
        // Expanded, the row PRINTS the person's name, and WCAG 2.5.3 (Label in
        // Name) then requires that visible text to be part of the accessible
        // name — otherwise someone driving the app by voice says the only word
        // they can see and reaches nothing. Collapsed there is no visible text
        // to match, so the control is named by what it does.
        aria-label={
          collapsed || !identity.label
            ? t("shell.accountAria")
            : `${identity.label} — ${t("shell.accountAria")}`
        }
        aria-expanded={open}
        // Collapsed there is no room to print who is signed in, so the sentence
        // is carried for a screen reader and clipped for the eye — the technique
        // the collapsed agent panel uses. It is wired as the DESCRIPTION rather
        // than left to be read in flow, because `aria-label` above replaces the
        // button's contents for name computation and would otherwise silence it.
        // The `title` is the pointer's version of the same sentence, and never
        // the accessible name.
        aria-describedby={collapsed && identity.spoken ? spokenId : undefined}
        title={collapsed && identity.spoken ? identity.spoken : undefined}
        onClick={() => setOpen((current) => !current)}
      >
        <span className="acctavatar">
          {identity.initials ?? <UserRound size={15} aria-hidden />}
        </span>
        <TriggerLines
          identity={identity}
          collapsed={collapsed}
          spokenId={spokenId}
        />
      </button>
      {open && (
        <div className="accountmenu" ref={menu}>
          <a href="#/settings/account">
            <UserRound size={15} aria-hidden />
            {t("shell.accountSettings")}
          </a>
          <a href="#/settings">
            <Settings size={15} aria-hidden />
            {t("nav.settings")}
          </a>
          <hr />
          <MenuPreferences />
          <hr />
          <button
            type="button"
            disabled={logout.isPending}
            onClick={() => logout.mutate()}
          >
            <LogOut size={15} aria-hidden />
            {t("shell.signOutAria")}
          </button>
        </div>
      )}
    </div>
  );
}
