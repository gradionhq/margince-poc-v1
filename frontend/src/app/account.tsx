import { ChevronsUpDown, LogOut, UserRound } from "lucide-react";
import { useCallback, useId, useRef, useState } from "react";
import type { components } from "../api/schema";
import { useT } from "../i18n";
import { useLogout, useMe } from "../screens/common";
import { usePopoverDismiss } from "./popover";
import "./account.css";

type SessionUser = components["schemas"]["MeResponse"]["user"];

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
 * The person, printed: their display name when the record carries one, otherwise
 * the address alone — which is then not repeated underneath itself.
 *
 * One spelling, because the trigger and the phone sheet both print it and a
 * second copy is how the two would come to disagree about which line is which.
 */
function IdentityLines({
  identity,
}: Readonly<{ identity: ReturnType<typeof identityOf> }>) {
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
 * This person's two rows: their own account, and the way out.
 *
 * Their account, and NOT a second row into settings generally. The rail already
 * carries that door, and a menu offering both put two rows a click apart that
 * landed on the same surface — one of them on its first entry, which is the page
 * the other one names. Two doors that mean different things is the shape worth
 * keeping; a third that means what one of them already means is not.
 *
 * At phone width this sheet is the ONLY way in, because the rail's door is
 * hidden there — the deep link still lands inside settings, where the page head's
 * section switcher opens every other entry.
 *
 * Shared by the menu and the phone sheet, so the two surfaces cannot offer
 * different rows, a different order, or a second way to sign out. The separator
 * is an `<hr>` rather than a border on the last row — it separates a group, and
 * a screen reader is told so.
 */
function AccountLinks() {
  const t = useT();
  const logout = useLogout();
  return (
    <>
      <a href="#/settings/account">
        <UserRound size={15} aria-hidden />
        {t("shell.account")}
      </a>
      <hr />
      <button
        type="button"
        disabled={logout.isPending}
        onClick={() => logout.mutate()}
      >
        <LogOut size={15} aria-hidden />
        {t("shell.signOutAria")}
      </button>
    </>
  );
}

/**
 * The same three rows, flat — what the phone sheet gets.
 *
 * At phone width the rail is a bottom bar and "More" expands it into a sheet
 * over the page. A popover anchored to the rail's foot has nowhere to open there,
 * so the sheet takes the rows themselves instead of the trigger that hides them.
 * The identity line comes with them: the sheet is the phone's whole sidebar, and
 * without the trigger nothing else on it says who is signed in.
 */
export function AccountRows() {
  const me = useMe();
  const identity = identityOf(me.data?.user);
  return (
    <div className="accountrows">
      {/* Nothing at all rather than an empty line while /me is in flight or
          carries neither a name nor an address. */}
      {identity.label && (
        <div className="acctsheetwho">
          <IdentityLines identity={identity} />
        </div>
      )}
      <AccountLinks />
    </div>
  );
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
      <IdentityLines identity={identity} />
      <ChevronsUpDown size={15} className="acctchev" aria-hidden />
    </>
  );
}

/**
 * The account block at the foot of the sidebar.
 *
 * It carries the three things it is FOR: this person's own account, the
 * installation's settings, and the way out. A menu rather than a row of chrome
 * buttons — the rail carries one account affordance, and sign-out beside every
 * screen invites a misclick. Theme and language are not here: they are
 * preferences rather than destinations, so they live on Settings → Account,
 * which is where a reader looks for them.
 *
 * It sits at the foot because that is where the person is, under the
 * destinations that are the product; the menu therefore opens UPWARD, out of the
 * trigger and over the navigation rather than off the bottom of the viewport.
 */
export function AccountMenu({ collapsed }: Readonly<{ collapsed: boolean }>) {
  const t = useT();
  const me = useMe();
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
          <AccountLinks />
        </div>
      )}
    </div>
  );
}
