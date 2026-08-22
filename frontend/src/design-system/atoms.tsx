import {
  ChevronRight,
  LoaderCircle,
  MoreHorizontal,
  Search,
} from "lucide-react";
import {
  type ComponentPropsWithRef,
  type CSSProperties,
  type ElementType,
  type FormEventHandler,
  type InputHTMLAttributes,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
  type RefObject,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import "./atoms.css";

// The Margince atom library (B-EP09.2, re-scoped to our own
// system, no gw-ui port; atoms are added as screens need them). Copy always
// arrives through props — callers translate with t(); atoms never hard-code
// user-facing words.

// `federated` is the door into another company's sign-in: full-width,
// unfilled, and carrying that company's own mark. It is a variant rather than a
// screen's own control because the alternative was tried — the sign-in surface
// hand-rolled a button with its own border, fill, radius, weight, padding,
// hover, focus and two dim states, and a control that redeclares every one of a
// variant's properties has left the design system rather than reused it.
type ButtonVariant = "primary" | "ghost" | "danger" | "federated";

/**
 * The turning mark a control shows while a write it started is in flight.
 *
 * Exported because `Switch` carries the same state and must draw it the same
 * way; sized by whatever control hosts it (`.btn svg` is 16px, 14px small), so
 * it takes no size prop. Decorative — `aria-busy` on the control is the fact,
 * and a glyph that announced itself would say it twice.
 */
export function BusyMark({ className }: Readonly<{ className?: string }>) {
  return (
    <LoaderCircle
      className={["busy-mark", className ?? ""].filter(Boolean).join(" ")}
      aria-hidden="true"
    />
  );
}

// A press that lands on a control already waiting for its own answer. Both
// halves are load bearing: `preventDefault` is what stops a `type="submit"`
// button posting the form a second time (a plain early return does not — the
// browser submits on the click, not on the handler), and `stopPropagation`
// stops a clickable row underneath treating the press as a click on itself.
//
// Aliased on import because this file also uses the DOM's own `MouseEvent`,
// for the document-level listener `OverflowMenu` attaches; the unaliased React
// type shadows it and that listener stops compiling.
function swallowWhileBusy(event: ReactMouseEvent<HTMLButtonElement>) {
  event.preventDefault();
  event.stopPropagation();
}

export function Button({
  variant = "ghost",
  small,
  iconOnly,
  className,
  reason,
  reasonId,
  unavailable,
  pending,
  busyLabel,
  disabled,
  ...rest
  // `ComponentPropsWithRef`, not the bare attribute set — the same reason
  // `TextInput` takes it: a caller that has to move focus to this control, or
  // restore it here after a dialog its own mutation removed the opener of,
  // needs the node. React 19 passes `ref` as an ordinary prop to a function
  // component, so this costs nothing but the type.
}: ComponentPropsWithRef<"button"> & {
  variant?: ButtonVariant;
  small?: boolean;
  /**
   * This button's whole label IS its icon, so it drops the width floor that
   * keeps a short word readable and becomes square. The caller still owes it an
   * accessible name — `aria-label` or a visually-hidden child — because a
   * glyph announces as nothing.
   */
  iconOnly?: boolean;
  /**
   * Why this action is unavailable. Passing it DISABLES the button and points
   * the control at the explanation with `aria-describedby` — a `title` on a
   * disabled button is announced by no screen reader, and a disabled button
   * cannot be focused, so a reason living only in `title` reaches nobody who
   * needed it. `Switch.reason` carries the same contract.
   *
   * STATE-4a decides WHEN to use it: a control blocked by state rather than
   * permission — an archived record, a frozen setting — stays visible and
   * says why, because the reason is the information and it can change.
   */
  reason?: string;
  /**
   * The id of an element ALREADY on the page carrying that explanation, for
   * a surface where several controls are refused by ONE fact. Printing the
   * same sentence beside every control states it as many times as there are
   * buttons; naming it once and pointing every control at it says it once
   * and still reaches a screen reader from each of them.
   */
  reasonId?: string;
  /**
   * A control the installation ADVERTISES and cannot complete.
   *
   * The RESTING refusal, and what separates it from the other two is how long
   * it lasts: `disabled` is a precondition that clears and the control comes
   * back, `pending` is a wait measured in seconds, and this is a door drawn
   * because the installation offers it with nothing behind it yet. It refuses
   * the press by itself, the way `reason` does, so the dead treatment cannot
   * end up drawn over a live control.
   *
   * It carries no sentence of its own — a caller that has one passes `reason`
   * as well and gets both — because the surface that needs this state may have
   * nothing it is allowed to say: the federated sign-in button is named by the
   * installation's own label, and a provider it advertises is not ours to
   * explain. So the drawing IS the whole claim, which is why it is a deeper
   * fade than `disabled` rather than the same one.
   */
  unavailable?: boolean;
  /**
   * Whether a write this button started is still in flight.
   *
   * It deliberately does NOT set `disabled`, and that is the whole contract.
   * Disabling the control the reader has just pressed detaches focus — Chrome
   * and Safari drop it to `<body>` — so the reader loses their place at the
   * exact moment the app has something to tell them, and an `aria-busy`
   * announcing the wait lands on a control nobody is on. `aria-disabled`
   * refuses the second press while keeping the button focusable, so focus
   * stays where the reader put it and the state is announced from there. The
   * press itself is swallowed in the handler, since `aria-disabled` is a
   * promise to assistive technology and not something the browser enforces.
   *
   * The LABEL does not change while this is set. The mark and `aria-busy`
   * already say "working"; swapping "Save" for "Saving…" says it a second time
   * and renames a focused control mid-press, which a screen reader re-reads —
   * so a caller passes one label and lets the state carry the rest.
   *
   * `reason` and `disabled` both outrank it: a control nobody may press cannot
   * also be mid-press, and drawing the mark there would claim a write nobody
   * started. That precedence is not cosmetic — a button carrying `disabled`
   * AND `pending` would be natively disabled, which drops the focus this whole
   * prop exists to keep, while announcing itself busy to a reader who is no
   * longer on it.
   */
  pending?: boolean;
  /**
   * What is happening, for a reader who cannot see the mark.
   *
   * `aria-busy` is the honest machine-readable state and this is deliberately
   * not a claim that it is spoken: ARIA defines it as "the element is being
   * modified, and assistive technology may want to wait before exposing the
   * change" — permission to DEFER, not an instruction to announce. Nothing
   * obliges a screen reader to say anything about it.
   *
   * So a screen that had something worth saying passes the sentence here and it
   * is added to `aria-describedby` while the write is out. Focus is on this
   * control — that is the premise of the whole prop — and a description arriving
   * on the focused element is announced, where a changed NAME would make the
   * reader re-hear the button itself. Copy arrives translated, like everything
   * else here.
   *
   * Optional because most buttons have nothing to add beyond "working". Not on
   * `Switch` yet: no surface has needed it there, and a prop with no caller is
   * an API nobody has tested.
   */
  busyLabel?: string;
}) {
  const ownReasonId = useId();
  const busyLabelId = useId();
  const classes = [
    "btn",
    `btn-${variant}`,
    small ? "btn-sm" : "",
    iconOnly ? "btn-icon" : "",
    unavailable ? "btn-unavailable" : "",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");
  const refused = reason !== undefined || reasonId !== undefined;
  // Every way this control can be barred, in one value, because `disabled` and
  // `busy` both have to agree about it. Refusal beats busy in ALL its
  // spellings: `disabled` used to be missing from this test, and the result was
  // the exact failure `pending` exists to prevent — a caller passing both got a
  // natively disabled button, focus gone, that announced itself busy and drew
  // the dimmed refused chrome with a spinner turning inside it. `unavailable`
  // joins them for the same reason: a door with nothing behind it cannot also
  // be mid-press. `Switch` reads the same way.
  const barred = refused || disabled === true || unavailable === true;
  const busy = pending === true && !barred;
  // Everything this component computes for itself is destructured out of
  // `rest`, so a caller's props cannot land on top of it. That is not tidiness:
  // a `disabled={false}` passed alongside `reason` re-enabled a control the
  // reason contract promises is refused; a caller's `aria-describedby` dropped
  // the pointer to the sentence saying why; an `onClick` surviving `pending`
  // let a second press through a button that is already writing. `aria-busy`
  // and `aria-disabled` join them because this component owns that state now —
  // a caller setting either by hand is describing something Button is already
  // describing, and the two can only disagree.
  const {
    "aria-describedby": callerDescribedBy,
    "aria-busy": _callerBusy,
    "aria-disabled": _callerAriaDisabled,
    onClick,
    children,
    ...attrs
  } = rest;
  const describedBy = describedByFor({
    callerDescribedBy,
    reason,
    reasonId,
    ownReasonId,
    busyLabelId: busy && busyLabel !== undefined ? busyLabelId : undefined,
  });
  const button = (
    <button
      type="button"
      {...attrs}
      className={classes}
      disabled={barred}
      aria-disabled={busy || undefined}
      aria-busy={busy || undefined}
      aria-describedby={describedBy}
      onClick={busy ? swallowWhileBusy : onClick}
    >
      {busy && <BusyMark />}
      {/* The children stay, ALWAYS. An icon-only control has no room for two
          16px marks side by side, so the glyph is hidden — but in CSS, by
          `.btn-icon[aria-busy="true"]`, because dropping the children here also
          dropped the visually-hidden text that `iconOnly` documents as one of
          the two ways to name such a button, leaving a focusable control with
          no accessible name at all. */}
      {children}
    </button>
  );
  return (
    <ButtonSentences
      reason={reason}
      reasonId={ownReasonId}
      busyLabel={busyLabel}
      busyLabelId={busyLabelId}
      busy={busy}
    >
      {button}
    </ButtonSentences>
  );
}

/**
 * What a Button's `aria-describedby` points at, in one place because three
 * sources compete for it and the precedence between them is the contract:
 * `reasonId` names an element the page already owns, `reason` renders its own
 * and outranks whatever the caller passed, and a caller's own description
 * survives only when nothing is refused. `busyLabel` is additive — a refused
 * control is never busy, so in practice it joins a caller's description or
 * stands alone.
 */
function describedByFor({
  callerDescribedBy,
  reason,
  reasonId,
  ownReasonId,
  busyLabelId,
}: Readonly<{
  callerDescribedBy?: string;
  reason?: string;
  reasonId?: string;
  ownReasonId: string;
  busyLabelId?: string;
}>): string | undefined {
  const refusal =
    reasonId ?? (reason === undefined ? callerDescribedBy : ownReasonId);
  return [refusal, busyLabelId].filter(Boolean).join(" ") || undefined;
}

/**
 * The sentences that belong to a button but may not live inside it.
 *
 * Anything rendered within a `<button>` joins its accessible NAME, so a
 * description of the wait placed there would rename the control mid-press —
 * the exact thing holding the label steady was for. Both sentences are
 * siblings instead, and the wrapper is `display: contents` when there is
 * nothing visible to stack, so a button that opts into `busyLabel` lays out
 * exactly as it did before.
 */
function ButtonSentences({
  reason,
  reasonId,
  busyLabel,
  busyLabelId,
  busy,
  children,
}: Readonly<{
  reason?: string;
  reasonId: string;
  busyLabel?: string;
  busyLabelId: string;
  busy: boolean;
  children: ReactNode;
}>) {
  if (reason === undefined && busyLabel === undefined) {
    return children;
  }
  return (
    <span className={reason === undefined ? "btn-shell" : "btn-with-reason"}>
      {children}
      {reason !== undefined && (
        <span id={reasonId} className="t-caption">
          {reason}
        </span>
      )}
      {/* Rendered whether or not the write is out, and emptied rather than
          removed. A description that arrives together with the element holding
          it is frequently missed; one that is already there and CHANGES is what
          a screen reader on the focused control actually reads. */}
      {busyLabel !== undefined && (
        <span id={busyLabelId} className="sr-only">
          {busy ? busyLabel : ""}
        </span>
      )}
    </span>
  );
}

export function Badge({
  tone,
  children,
  quiet,
}: Readonly<{
  tone?: "success" | "warn" | "danger" | "ai" | "accent";
  children: ReactNode;
  // The same status in a column of them. A pill states one status against
  // surrounding prose; a table row states one per row, and a stack of filled
  // pills reads as decoration a reader learns to skip. `quiet` keeps the tone
  // and drops the fill: a dot in the tone's colour, and the label as plain
  // text. Same vocabulary, so a status cannot be worded one way in a list and
  // another on the record the list opens.
  quiet?: boolean;
}>) {
  const classes = ["badge"];
  if (quiet) {
    classes.push("badge-quiet");
  }
  if (tone) {
    classes.push(`badge-${tone}`);
  }
  return <span className={classes.join(" ")}>{children}</span>;
}

// AVATAR_TONES are the monogram backgrounds, all token-driven. The colour
// is picked from the record, not stored, so the same record looks the same on
// every screen and in every session without a round trip.
const AVATAR_TONES = 6;

/**
 * The initials a chip falls back to.
 *
 * Split on whitespace AND on the punctuation an address uses, because the
 * signed-in reader is frequently known to the product only by their address:
 * `jane.doe@example.com` reads as "JD" here, where a whitespace-only split
 * gives the single letter "J" and every colleague whose address starts with a
 * J gets the same chip. Two letters at most — a third stops being a monogram
 * and starts being text set too small to read.
 */
function monogramOf(name: string): string {
  return name
    .split(/[\s@._-]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => [...part][0]?.toUpperCase() ?? "")
    .join("");
}

export function Avatar({
  name,
  identity,
  src,
  size = "sm",
}: Readonly<{
  name: string;
  /**
   * What the tint is derived FROM, when that is not the displayed name — a
   * record id, an address, anything stable for the life of the record. The
   * name is the fallback and it is a poor key: renaming a person or a company
   * silently moves them to a different colour on every screen at once, which
   * reads as a different record rather than as a rename.
   */
  identity?: string;
  // A resolved logo to render instead of the monogram. The monogram is the
  // floor, not the fallback of last resort: it is what shows while the image
  // loads, if it fails to load, and whenever no logo resolved — so a company
  // is never a broken image or an empty slot.
  src?: string | null;
  /**
   * The four sizes this chip is drawn at, which used to be four numbers in
   * four different stylesheets for a prop that admitted two: `xs` in a dense
   * table, `sm` in every list row and beside every name, `md` on a record
   * header, `lg` on a wide one.
   */
  size?: "xs" | "sm" | "md" | "lg";
}>) {
  // An image that fails to load falls back to the monogram for the rest of
  // this mount. Keyed by src so a record whose logo changes gets a fresh try
  // rather than inheriting the previous one's failure.
  const [brokenSrc, setBrokenSrc] = useState<string | null>(null);
  const broken = Boolean(src) && brokenSrc === src;
  const setBroken = () => setBrokenSrc(src ?? null);
  // The monogram is the floor UNDER the mark, so it has to stop being drawn the
  // moment the mark is actually on screen: a logo with transparency would
  // otherwise show the initials through it. Tracked by src for the same reason
  // as the failure above.
  const [paintedSrc, setPaintedSrc] = useState<string | null>(null);
  // A mark that painted once and then failed on a later load is no longer on
  // screen, so it stops holding the monogram down: without the `!broken` the
  // image is removed while the fallback stays suppressed, and the avatar is
  // simply empty.
  const painted = Boolean(src) && paintedSrc === src && !broken;
  const initials = monogramOf(name);
  // A small sum over the code points: stable across sessions and locales, and
  // the spread only has to be even enough that neighbouring records in a list
  // rarely collide.
  //
  // The tint is UNCONDITIONAL. It used to be opt-in, and the result was that a
  // company was tinted in the list it was found in and a neutral accent chip on
  // the record page that list opened — the same company, two colours, one
  // click apart. A chip that identifies a record on one screen and not on the
  // next identifies nothing.
  let tone = 0;
  for (const char of identity ?? name) {
    tone = (tone + (char.codePointAt(0) ?? 0)) % AVATAR_TONES;
  }
  const classes = ["avatar", `avatar-t${tone}`, `avatar-${size}`];
  if (src && !broken) classes.push("avatar-has-logo");
  if (painted) classes.push("avatar-painted");
  return (
    <span className={classes.join(" ")}>
      {src && !broken ? (
        // The monogram stays underneath: it is what the chip shows until the
        // image paints, and what is left if the image never does.
        <img
          className="avatar-img"
          src={src}
          alt=""
          loading="lazy"
          onError={setBroken}
          onLoad={() => setPaintedSrc(src ?? null)}
        />
      ) : null}
      {!painted && initials}
    </span>
  );
}

// `ComponentPropsWithRef`, not the bare attribute set: a caller that opens
// this field itself — an edit-in-place value that has to put the caret where
// the reader just clicked — needs the node, and React 19 passes `ref` as an
// ordinary prop to a function component.
export function TextInput(props: ComponentPropsWithRef<"input">) {
  return (
    <input {...props} className={`input ${props.className ?? ""}`.trim()} />
  );
}

export function SearchField(props: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <span className="input-icon">
      <Search aria-hidden />
      <input
        type="search"
        {...props}
        className={`input ${props.className ?? ""}`.trim()}
      />
    </span>
  );
}

/**
 * Textarea carries no label of its own, exactly like TextInput: the label is
 * composed outside it, by the `.field` wrapper a form uses or by a screen's own
 * richer shell. What it owns is the ONE spelling of the control's surface, so a
 * note field in a create form and one in settings cannot drift.
 *
 * The dropdown is NOT here: `Select` in select.tsx is a button and a portalled
 * listbox, because a native `<select>` draws its own option list in the
 * platform's idiom and no CSS reaches inside it. It still reads `.input` for its
 * closed face — a dropdown and a text input are the same field on screen.
 *
 * `ComponentPropsWithRef`, for the same reason `TextInput` takes it: a caller
 * that has to move focus HERE needs the node, and a dialog whose fields are
 * mostly prose has no single-line input to land on instead.
 */
export function Textarea(props: ComponentPropsWithRef<"textarea">) {
  return (
    <textarea
      {...props}
      className={`textarea ${props.className ?? ""}`.trim()}
    />
  );
}

/**
 * Checkbox and Radio DO carry their label, and that is the difference from the
 * fields above: for a tick the label is not a caption sitting nearby, it is the
 * other half of the click target. Wrapping the input is what makes the words
 * clickable and what gives the control its accessible name without an `id` to
 * thread — which is why seventeen of the twenty hand-rolled sites already wrote
 * this shape, each with its own wrapper class and its own idea of the gap.
 *
 * `label` is a ReactNode, not a string: a consent line carries emphasis and a
 * settings toggle carries a help line under the name.
 *
 * `className` lands on the LABEL, not the input, because that is where every
 * existing call site puts its layout — a row that needs `align-items:flex-start`
 * for a two-line label says so there.
 */
type ToggleProps = Omit<InputHTMLAttributes<HTMLInputElement>, "type"> & {
  label: ReactNode;
};

function Toggle({
  kind,
  label,
  className,
  ...rest
}: ToggleProps & { kind: "checkbox" | "radio" }) {
  return (
    <label
      className={["checkfield", className ?? ""].filter(Boolean).join(" ")}
    >
      <input type={kind} {...rest} />
      <span>{label}</span>
    </label>
  );
}

export function Checkbox(props: ToggleProps) {
  return <Toggle kind="checkbox" {...props} />;
}

export function Radio(props: ToggleProps) {
  return <Toggle kind="radio" {...props} />;
}

/**
 * What a Field hands its control: the id its label points at, the required
 * state, and the hint to describe it by. Callers spread it whole rather than
 * picking pieces, so a field that later grows a hint wires it up without the
 * call site changing.
 */
export type FieldControl = Readonly<{
  id: string;
  required?: boolean;
  "aria-describedby"?: string;
  /**
   * Whether the value currently in the control was refused. Set from `error`,
   * so a caller that spreads the control whole announces the refusal from the
   * control itself rather than only printing it underneath.
   */
  "aria-invalid"?: boolean;
}>;

/**
 * Field is the label-above-control row every form is built from.
 *
 * It owns the id. Before this, each call site minted its own — `${formId}-role`,
 * `${headingId}-expiry`, a hardcoded "overlay-region" — and had to remember to
 * repeat it in two places; a typo in either half silently unlabels the control,
 * and nothing fails. `useId` removes the chance to get it wrong.
 *
 * The label is a real `<label>` with `htmlFor`, which is the other reason this
 * exists: eleven call sites drew the same row with a `<span>` and pointed at it
 * with `aria-labelledby`. That announces correctly but is not a label — clicking
 * the words does not focus the control, and the browser's own form semantics
 * never engage.
 *
 * The hint sits OUTSIDE the label deliberately. Inside, it would be swallowed
 * into the control's accessible name, so a reader would hear the entire help
 * text every time focus lands.
 *
 * `required` marks the label and the control from one prop. The asterisk is
 * `aria-hidden` because the control's own `required` already announces the
 * state — spelling it twice is how a field ends up read as "Role star required".
 */
export function Field({
  label,
  labelEnd,
  hint,
  hintLive,
  error,
  icon,
  trailing,
  required,
  className,
  children,
}: Readonly<{
  // A node, not a string: a label is usually words, but a field whose value was
  // read from somewhere carries its provenance in the label row — a confidence
  // meter and a source chip beside the name.
  label: ReactNode;
  /**
   * What sits at the far end of the label's own line — the "Forgot?" link
   * beside a password, a unit beside an amount. It belongs to the label ROW
   * rather than to the label, so it is not swallowed into the control's
   * accessible name.
   */
  labelEnd?: ReactNode;
  hint?: string;
  /**
   * Announce the hint when it CHANGES, not only when focus reaches the field.
   *
   * Off by default, because a rule that is always true is a description and a
   * description read on every focus is noise. On for a hint that appears in
   * response to something the reader just did — the caps-lock warning under a
   * password is the case this exists for, and the reason is in its timing: caps
   * lock gets pressed while typing, so a warning a reader only hears if they
   * leave the field and come back has arrived after the password it was about.
   */
  hintLive?: boolean;
  /**
   * Why the value in this field was refused.
   *
   * A separate slot from `hint` because the two say different things and were
   * being spelled the same way: the password forms put "too short" and "the
   * passwords do not match" through `hint`, so a refusal rendered in the same
   * meta-grey as neutral helper text, and on one screen in the same grey as the
   * SUCCESS line four elements above it. This one announces, marks the control
   * `aria-invalid`, and reads in the danger tone — and the hint stays visible
   * beside it, because what the field wants is still true while it is wrong.
   */
  error?: string;
  /**
   * A leading affordance INSIDE the control's outline — a mail glyph on an
   * address field. Decorative: the label names the field.
   */
  icon?: ReactNode;
  /**
   * A control inside the outline at the trailing end — the password reveal.
   *
   * This is what `auth.tsx` forked its own `Field` for. `.input-icon` could
   * carry a leading glyph and nothing else, so a button that has to sit inside
   * the focus ring had no way to get there, and a second field component grew
   * on the sign-in screens with its own label size, its own gap and its own
   * hint that was never wired to `aria-describedby`.
   */
  trailing?: ReactNode;
  required?: boolean;
  // Layout the surrounding form owns — a width, a grid span, a screen's own
  // field modifier. It lands on the wrapper, which is the only element a
  // caller has any business positioning.
  className?: string;
  children: (control: FieldControl) => ReactNode;
}>) {
  const id = useId();
  const hintId = hint ? `${id}-hint` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  // Both, when both are on screen. A field that is refused AND still carrying
  // its rule has two things to say, and naming only one of them in
  // `aria-describedby` picks which sighted and non-sighted readers get.
  const describedBy = [errorId, hintId].filter(Boolean).join(" ") || undefined;
  const control = children({
    id,
    required,
    "aria-describedby": describedBy,
    "aria-invalid": error ? true : undefined,
  });
  return (
    <div className={["field", className ?? ""].filter(Boolean).join(" ")}>
      {labelEnd ? (
        <span className="field-label-row">
          <label className="t-label" htmlFor={id}>
            {label}
            {required && <span aria-hidden> *</span>}
          </label>
          {labelEnd}
        </span>
      ) : (
        <label className="t-label" htmlFor={id}>
          {label}
          {required && <span aria-hidden> *</span>}
        </label>
      )}
      {/* The shell exists ONLY when something has to sit inside the outline. A
          field with neither affordance emits exactly the markup it always did,
          which is what keeps the two hundred existing call sites unchanged —
          the border and the focus ring stay on the input there, and move to the
          shell only where an icon or a control would otherwise sit beside the
          outline rather than within it. */}
      {icon || trailing ? (
        <span className="field-shell">
          {icon && (
            <span className="field-shell-icon" aria-hidden>
              {icon}
            </span>
          )}
          {control}
          {trailing}
        </span>
      ) : (
        control
      )}
      {error && (
        <p className="field-error" id={errorId} role="alert">
          {error}
        </p>
      )}
      {hint && (
        <p
          className="t-caption"
          id={hintId}
          role={hintLive ? "status" : undefined}
        >
          {hint}
        </p>
      )}
    </div>
  );
}

/**
 * StatCard is one reading at the top of a record: a label, the reading itself,
 * and one line of detail saying what it is drawn from.
 *
 * The detail line is not decoration. A reading with no basis stated is a number
 * a reader has to trust, and this surface exists because a number nobody could
 * scale — "Relationship 2/100" — was doing exactly that.
 *
 * `tone` colours the value, never the whole tile: a strip of coloured boxes
 * reads as a dashboard, and the reader is meant to see three facts, not a
 * traffic light.
 *
 * `alert` is the one exception, for the one slot whose reading is bad news
 * simply by being present (an overdue balance, a lapsed renewal) rather than
 * by its number — those tint the whole tile, because there is no value to
 * colour that says the same thing on its own. Wire it only there; a reading
 * that could be read either way stays plain and lets its own words carry the
 * judgement.
 */
export function StatCard({
  label,
  value,
  detail,
  tone,
  source,
  alert,
  dot,
  numeric,
}: Readonly<{
  label: string;
  value: string;
  detail?: string;
  // `good` is not "no tone": a slot whose reading is a VERDICT says so in both
  // directions, and a verdict that is fine reads as fine rather than as one
  // nobody has judged yet.
  tone?: "good" | "warn" | "danger";
  // Where the figure came from, named on the card that shows it. A money
  // reading a reader cannot trace is one they have to go and verify
  // elsewhere, which is the trip the badge saves them.
  source?: ReactNode;
  // Tints the whole tile. See the docblock above — this is not `tone` at
  // stronger volume, it is a different judgement (the slot itself is bad
  // news, not just its figure).
  alert?: boolean;
  // A small coloured mark before the value, for the one slot whose reading
  // is a VERDICT rather than a figure — a glance a reader can catch without
  // reading the word. Gated on `tone` as well as this flag, never on its
  // own: the colour and the decision to show it at all come from the same
  // judgement, so a fine verdict can never carry a leftover dot.
  dot?: boolean;
  // The reading is a FIGURE — money, a count, a duration — so it draws in the
  // mono face, where digits share one width and a column of readings lines up
  // instead of shifting slot to slot with every comma.
  //
  // A flag rather than a `ReactNode` value: the value stays a string this
  // component owns the type of. Widened to a node, the face would be the
  // caller's to spell, and a screen that spells type is the second author of a
  // scale this tier owns — which is also markup arriving at a slot the copy and
  // colour gates read as a string.
  numeric?: boolean;
}>) {
  const valueClass = [
    "stat-card-value",
    "t-h3",
    numeric ? "t-mono" : "",
    tone ? `stat-card-${tone}` : "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <section className={alert ? "stat-card stat-card-alert" : "stat-card"}>
      <span className="stat-card-label t-caption">
        {label}
        {source && <span className="stat-card-source">{source}</span>}
      </span>
      <span className={valueClass}>
        {dot && tone && (
          <span
            className={`stat-card-dot stat-card-dot-${tone}`}
            aria-hidden="true"
          />
        )}
        {value}
      </span>
      {detail && <span className="stat-card-detail t-caption">{detail}</span>}
    </section>
  );
}

// The element a card wraps its content in. A card is a section of the page by
// default; the other four exist because a card sometimes IS the form you submit,
// the item in a list, or a plain grouping box that must not add a section to the
// document outline.
type CardElement = "section" | "div" | "article" | "form" | "li";

/**
 * The one card in the product: elevated ground, subtle border, 12px radius, one
 * padding. Every surface that reads as a card comes from here — a hand-rolled
 * `<div className="card">` drifts the moment one of those five values changes.
 *
 * `title`/`sub`/`actions` render the card's SectionHeader, so the header sits at
 * the top of the card's own padding without the caller re-deriving that; a card
 * whose head is genuinely bespoke passes children only.
 */
export function Card({
  as = "section",
  inset,
  title,
  sub,
  actions,
  level,
  children,
  className,
  style,
  id,
  ariaLabel,
  role,
  testId,
  onSubmit,
}: Readonly<{
  as?: CardElement;
  inset?: boolean;
  title?: string;
  sub?: string;
  actions?: ReactNode;
  // Passed straight to the card's SectionHeader. A card nested inside a
  // section that already has an h2 passes 3, so the outline says "inside"
  // rather than "beside" — see SectionHeader's own note.
  level?: 1 | 2 | 3;
  children?: ReactNode;
  className?: string;
  style?: CSSProperties;
  id?: string;
  // Naming the card makes it a region a screen reader can land on and list;
  // spelled out rather than spread so the prop reads the same at every call.
  ariaLabel?: string;
  // A card that ANNOUNCES itself: an advisory the app raises while the reader is
  // elsewhere on the page has to reach a screen reader without stealing focus,
  // and that is a live region on the card itself — wrapping it in one would add
  // a node that exists only to satisfy this component.
  role?: "status";
  testId?: string;
  // Only meaningful with `as="form"` — a card that is the form it submits.
  onSubmit?: FormEventHandler<HTMLElement>;
}>) {
  const Tag: ElementType = as;
  return (
    <Tag
      className={["card", inset ? "card-inset" : "", className ?? ""]
        .filter(Boolean)
        .join(" ")}
      style={style}
      id={id}
      aria-label={ariaLabel}
      role={role}
      data-testid={testId}
      onSubmit={onSubmit}
    >
      {title !== undefined && (
        <SectionHeader
          title={title}
          sub={sub}
          actions={actions}
          level={level}
        />
      )}
      {children}
    </Tag>
  );
}

export function Skeleton({
  width,
  height = 14,
}: Readonly<{
  width: number | string;
  height?: number;
}>) {
  return <div className="skeleton" style={{ width, height }} />;
}

// The placeholder lines, named rather than counted: a line's identity IS its
// position, and naming them gives the list a stable key without reaching for
// the array index. Eight is the ceiling on purpose — a wait that needs more room
// than eight lines of text is a shape (a table, a chart, a form), and more bars
// is the wrong answer to it.
const PENDING_LINES = [
  "first",
  "second",
  "third",
  "fourth",
  "fifth",
  "sixth",
  "seventh",
  "eighth",
] as const;

/**
 * The pending state of a surface — the ONE spelling of it in this product.
 *
 * Four grew before this: `QueryStates`' three inline-styled bars, `SurfaceState`'s
 * single silent 32px bar, `ListTable`'s five unanimated bone rows, and a page's
 * worth of hand-rolled bars and "Loading…" lines. They disagreed about the shape,
 * about the height, about whether the pulse ran at all, and — the part that
 * mattered — about whether a reader who cannot see the bars is told anything.
 * Three call sites had already bolted their own `sr-only` line beside one of
 * these, which is the tell that the primitive was missing something rather than
 * that those screens were special.
 *
 * `label` is REQUIRED and not defaulted. A placeholder carries no text, so the
 * spoken line is the only thing a screen reader has; making it a required prop
 * is what stops the next pending state from being silent. It is also the caller
 * who knows what is being waited for, and "Loading the review queue…" is worth
 * more than "Loading…" to someone who cannot see which part of the page went
 * grey.
 *
 * `lines` is a HEIGHT RESERVATION, not decoration: it is how many rows of
 * content will stand here once the read answers. Under-reserving is the reason
 * a card jumps when its body arrives, and the jump is worse than the wait —
 * a reader who has started reading loses their place. Over-reserving costs a
 * collapse in the other direction, so the honest number is the content's own
 * usual size, and 3 is the default only because it is the commonest.
 *
 * `visible` shows the label above the bars instead of only speaking it, for a
 * wait long enough that a mute grey block reads as broken rather than as
 * working — a first assessment that includes a model call is upwards of twenty
 * seconds cold. It is a flag rather than a second string because the sentence is
 * the same sentence: two of them is how a screen reader ends up hearing the wait
 * announced twice.
 */
export function PendingBody({
  label,
  lines = 3,
  visible,
}: Readonly<{ label: string; lines?: number; visible?: boolean }>) {
  return (
    <div className="pending" role="status" aria-busy="true">
      {/* The label lands EITHER on the page or in the accessibility tree alone,
          never both: this is a live region, and the same sentence twice inside
          it is announced twice. */}
      {visible ? (
        <p className="t-small pending-note">{label}</p>
      ) : (
        <span className="sr-only">{label}</span>
      )}
      {PENDING_LINES.slice(0, lines).map((line) => (
        <div key={line} className="skeleton pending-line" />
      ))}
    </div>
  );
}

/**
 * EmptyState is the one "nothing here" plate.
 *
 * Bare, it is a one-liner: the caller's sentence, centred, in the meta tone —
 * the shape a filtered list or a section with no rows takes. With `title` it
 * becomes the INSTRUCTIONAL variant a first-run surface needs: a heading that
 * names what the page holds, the caller's paragraph saying how a record of
 * this kind comes to exist, and the one primary `action` that makes the first
 * one. The two are one component rather than two because they are the same
 * plate with more or less on it, and a second spelling of the plate is how a
 * page's first-run state came to look like a different product from its
 * filtered-empty state.
 *
 * The words stay the caller's, translated with `t()`; nothing here knows what
 * kind of record is missing.
 */
export function EmptyState({
  title,
  action,
  children,
}: Readonly<{
  // The instructional variant's heading. Present, the children render as the
  // explanatory paragraph under it rather than as the whole plate.
  title?: string;
  // The one verb that ends the empty state — a create button. Rendered only
  // with `title`: a bare one-liner that offered a verb would be a filtered
  // list inviting the reader to create what the filter hid.
  action?: ReactNode;
  children: ReactNode;
}>) {
  if (title === undefined) {
    return (
      <Card as="div" inset className="empty">
        {children}
      </Card>
    );
  }
  return (
    <Card as="div" inset className="empty empty-instructional">
      <h2 className="t-h2 empty-title">{title}</h2>
      <div className="empty-body">{children}</div>
      {action && <div className="empty-action">{action}</div>}
    </Card>
  );
}

export function SectionHeader({
  title,
  sub,
  actions,
  level = 2,
}: Readonly<{
  title: string;
  sub?: string;
  // Controls that act on this section, placed beside the title stack rather
  // than under it. A caller that needs them anywhere else lays them out itself.
  actions?: ReactNode;
  // A section heading by default. `1` is for the one header on a page that IS
  // the page's name — a record surface the app shell deliberately yields to,
  // where this title is the only thing naming the page. Every other header on
  // that page stays at level 2, so a document never carries two page titles.
  //
  // `3` is a section INSIDE a section: a group of fields under a settings
  // page's own h2, an "add a connection" block inside the connectors card.
  // Without it those headers were h2s nested in an h2, which tells a screen
  // reader the inner block is a sibling of the page's own section — the
  // outline says the group is as important as the page it sits in, and a
  // reader navigating by heading cannot tell where they are. The type follows
  // the level down with it: an inner heading that is the same size as its
  // parent is the same defect drawn instead of announced.
  level?: 1 | 2 | 3;
}>) {
  return (
    <div className="section-header">
      <div className="section-header-text">
        {level === 1 && <h1>{title}</h1>}
        {level === 2 && <h2>{title}</h2>}
        {level === 3 && <h3>{title}</h3>}
        {sub && <span className="sub">{sub}</span>}
      </div>
      {actions && <div className="section-header-actions">{actions}</div>}
    </div>
  );
}

export function SegmentedControl<Option extends string>({
  options,
  value,
  onChange,
  labels,
  label,
}: Readonly<{
  options: readonly Option[];
  value: Option;
  onChange: (next: Option) => void;
  labels: Record<Option, string>;
  // Accessible name for the control as a whole (the `fieldset` group); a
  // screen reader announces it alongside each option so the buttons aren't
  // read out of context. Optional so existing callers are unaffected.
  label?: string;
}>) {
  return (
    <fieldset className="segmented" aria-label={label}>
      {options.map((option) => (
        <button
          key={option}
          type="button"
          aria-pressed={option === value}
          onClick={() => onChange(option)}
        >
          {labels[option]}
        </button>
      ))}
    </fieldset>
  );
}

export function Kbd({ children }: Readonly<{ children: ReactNode }>) {
  return <kbd className="kbd">{children}</kbd>;
}

export function Modal({
  open,
  onClose,
  labelledBy,
  size = "default",
  placement = "center",
  returnFocusTo,
  children,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  labelledBy: string;
  // "wide" roomier variant for content-dense dialogs (code/YAML previews);
  // "default" keeps the compact form width every confirm/create modal uses.
  size?: "default" | "wide";
  // "right" anchors the dialog to the right edge, full height — the drawer
  // form the composer and the evidence receipt use, where the record behind
  // stays visible as context rather than being covered by a centred box.
  // With size="wide" it takes the roomier clamp and a sticky header/footer,
  // for the surfaces a rep works IN rather than glances at.
  placement?: "center" | "right";
  // Where focus should land instead of the opener, for a dialog whose OWN
  // mutation removes the control that opened it — a Deactivate button that
  // becomes Reactivate, a row the delete drops from the list.
  //
  // A callback rather than a ref because the resting place frequently does not
  // exist while the dialog is open: it is produced by the very mutation the
  // dialog performs, and the opener is detached by the time anything could ask
  // about it. Resolving at restore time is the only moment the answer is known.
  // A caller that does hold a ref passes `() => ref.current`, so the ref form
  // is a subset of this one rather than a second API.
  returnFocusTo?: () => HTMLElement | null;
  children: ReactNode;
}>) {
  const dialog = useRef<HTMLDivElement | null>(null);
  // Held in a ref so the restore below calls the CURRENT resolver. The focus
  // effect is keyed on `open` alone — re-running it whenever an inline callback
  // changes identity would drag focus back to the dialog's first stop on every
  // render of the page behind it — so the closure it captures is otherwise the
  // one from the render that opened the dialog.
  const returnFocus = useRef(returnFocusTo);
  useEffect(() => {
    returnFocus.current = returnFocusTo;
  }, [returnFocusTo]);
  // A LAYOUT effect, because a passive one is scheduled after the browser
  // paints: between the commit that puts this dialog on screen and a passive
  // effect attaching the listener, the dialog is visible, hit-testable, and
  // deaf to Escape. A key pressed in that window is not queued, it is lost —
  // and for a dialog whose dismissal is the safe answer, losing it strands the
  // reader in front of a question that no longer answers the one key everyone
  // reaches for. React runs a layout effect during the commit, before yielding
  // to the browser, so there is no frame in which this dialog can be seen and
  // not respond.
  useLayoutEffect(() => {
    if (!open) {
      return;
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
        return;
      }
      if (event.key === "Tab" && dialog.current) {
        keepTabInside(event, dialog.current);
      }
    };
    globalThis.addEventListener("keydown", onKey);
    return () => globalThis.removeEventListener("keydown", onKey);
  }, [open, onClose]);

  // Focus moves in when the dialog opens and returns to whatever opened it
  // when it closes — otherwise a keyboard reader who dismisses a dialog
  // resumes tabbing from the top of the document, having lost their place.
  useEffect(() => {
    if (!open) {
      return;
    }
    const opener = document.activeElement;
    const stops = dialog.current ? focusableWithin(dialog.current) : [];
    (stops[0] ?? dialog.current)?.focus();
    return () => {
      // A named target outranks the opener even while the opener is still
      // attached: a caller names one precisely because the mutation this dialog
      // just performed unmakes that control, and the unmaking usually lands
      // with the refetch a moment AFTER this runs. Handing focus back to a
      // button that is about to be removed drops the reader on <body> a tick
      // later, which is the failure this whole escape hatch exists to prevent.
      const named = returnFocus.current?.() ?? null;
      if (named?.isConnected) {
        named.focus();
        return;
      }
      // focus() on a node the mutation already detached is a silent no-op and
      // leaves focus on <body>, from where the next Tab restarts at the top of
      // the document. Asking first is what keeps that case from looking like a
      // restore that worked.
      if (opener instanceof HTMLElement && opener.isConnected) {
        opener.focus();
      }
    };
  }, [open]);

  if (!open) {
    return null;
  }
  // Portalled to the document body rather than rendered in place: a dialog
  // opened from inside a collapsed container — the record header's overflow
  // menu — would otherwise be hidden along with it, and the click that opened
  // the dialog is the same click that collapses the menu.
  return createPortal(
    // biome-ignore lint/a11y/noStaticElementInteractions: backdrop dismiss is a convention; Esc is the keyboard path
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc handles the keyboard path above
    <div // NOSONAR: backdrop dismiss only; keyboard path (Esc) handled by the effect above
      className={placement === "right" ? "overlay overlay-right" : "overlay"}
      onClick={(event) => {
        if (event.target === event.currentTarget) {
          onClose();
        }
      }}
    >
      <div
        // NOSONAR: styled modal overlay driven by React state, not a native <dialog>; conversion would change focus/backdrop behavior
        role="dialog"
        aria-modal="true"
        aria-labelledby={labelledBy}
        className={modalClass(size, placement)}
        ref={dialog}
        // Focusable so a dialog whose body is pure text still receives focus
        // when it opens, rather than leaving it on the page behind.
        tabIndex={-1}
      >
        {children}
      </div>
    </div>,
    document.body,
  );
}

// A right-anchored dialog draws its width from the viewport, so the `size`
// variants — which exist to widen a centred box — do not apply to it.
function modalClass(size: "default" | "wide", placement: "center" | "right") {
  if (placement === "right") {
    // A drawer's width normally comes from the viewport, but a surface a rep
    // WORKS in — a numbered claim list, a message being written — wraps into an
    // unreadable column at the default clamp. `size` is what asks for the
    // roomier one, and it brings sticky header and footer with it.
    return size === "wide"
      ? "modal modal-drawer modal-drawer-wide"
      : "modal modal-drawer";
  }
  return size === "wide" ? "modal modal-wide" : "modal";
}

// Keep Tab inside the dialog. `aria-modal` tells a screen reader the rest of
// the page is inert; it does nothing for the Tab key, so without this a
// keyboard reader walks straight out of the dialog into the page behind it and
// can operate a surface the dialog is covering.
function keepTabInside(event: KeyboardEvent, dialog: HTMLElement) {
  const stops = focusableWithin(dialog);
  if (stops.length === 0) {
    event.preventDefault();
    return;
  }
  const first = stops[0];
  const last = stops[stops.length - 1];
  const active = document.activeElement;
  // Focus already outside the dialog is the case both directions have to
  // catch, not just Shift+Tab: it happens whenever something on the page
  // behind took focus while the dialog was open, and from there a plain Tab
  // would keep walking that page rather than coming back.
  const outside = !dialog.contains(active);
  const leavingBackwards = event.shiftKey && (active === first || outside);
  const leavingForwards = !event.shiftKey && (active === last || outside);
  if (!leavingBackwards && !leavingForwards) {
    return;
  }
  event.preventDefault();
  (leavingBackwards ? last : first).focus();
}

// The tab stops inside a container, in document order. Disabled controls and
// anything explicitly removed from the tab order are not stops; a negative
// tabindex (the dialog's own) is reachable by script but not by Tab, so it is
// deliberately excluded here.
const FOCUSABLE =
  'a[href], button, input, select, textarea, summary, [tabindex]:not([tabindex="-1"])';

function focusableWithin(root: HTMLElement): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
    (element) => !element.hasAttribute("disabled") && element.tabIndex !== -1,
  );
}

// The attainment band the server computes (met ≥ 100, accent 60–99,
// behind < 60). The ring and any echoing Badge take this verbatim — the
// client never recomputes it from a raw percentage.
export type AttainmentBand = "met" | "accent" | "behind";

const BAND_STROKE: Record<AttainmentBand, string> = {
  met: "var(--online)",
  accent: "var(--accent)",
  behind: "var(--away)",
};

// AttainmentRing (RD-PARAM-4): an SVG progress ring whose arc length reflects
// `pct` (the server's raw, uncapped attainment percentage) capped at a full
// circle, and whose colour is the server-computed `band` — never re-derived
// here from `pct`. Pure and prop-driven: no fetch, so Storybook and tests
// render it directly. The centred figure is the real rounded percentage
// (which can read past 100%) above a caption slot.
export function AttainmentRing({
  pct,
  band,
  caption,
}: Readonly<{
  pct: number;
  band: AttainmentBand;
  caption: string;
}>) {
  const radius = 68;
  const circumference = 2 * Math.PI * radius;
  const fraction = Math.min(pct / 100, 1);
  const offset = circumference * (1 - fraction);
  return (
    <div className="attain-ring">
      <svg width={160} height={160} viewBox="0 0 160 160" aria-hidden="true">
        <circle
          cx={80}
          cy={80}
          r={radius}
          fill="none"
          stroke="var(--bgCard)"
          strokeWidth={14}
        />
        <circle
          cx={80}
          cy={80}
          r={radius}
          fill="none"
          stroke={BAND_STROKE[band]}
          strokeWidth={14}
          strokeLinecap="round"
          strokeDasharray={circumference}
          strokeDashoffset={offset}
        />
      </svg>
      <div className="attain-ring-center">
        <span className="attain-ring-pct t-mono">{Math.round(pct)}%</span>
        <span className="attain-ring-lbl">{caption}</span>
      </div>
    </div>
  );
}

/** Whether a box is holding more width than it is showing. */
function overflowsSideways(element: HTMLElement | null): boolean {
  return element !== null && element.scrollWidth - element.clientWidth > 1;
}

/** Spread onto the scrolling box. Empty while it has nothing hidden to reach. */
type ScrollRegion = Readonly<{
  tabIndex?: 0;
  role?: "region";
  "aria-label"?: string;
}>;

/**
 * Make a box that scrolls sideways reachable, and only then.
 *
 * A region holding content past its right edge is content pointer users can
 * drag to and keyboard users cannot reach at all, so it takes a tab stop and
 * announces itself by name. It takes neither while it fits: a tab stop in front
 * of every table in the product, most of which fit, is a cost every keyboard
 * reader pays for the few that do not. That is the same bargain
 * `useTruncationTooltip` strikes for a string that fits its row.
 *
 * Both spellings of a scrolling table body use this — `TableScroll` below, and
 * the list surface's own `.lt-scroll` (listtable.tsx) — so a reader meets the
 * same behaviour whichever table they land in.
 */
export function useScrollRegion(
  box: RefObject<HTMLElement | null>,
  label: string,
): ScrollRegion {
  const [scrolls, setScrolls] = useState(false);
  const [watched, setWatched] = useState<HTMLElement | null>(null);
  // Measured after every render rather than when the rows change: the answer
  // moves for reasons this hook never sees — a column the reader dragged, a
  // cell whose badge arrived — and re-reading it is two property reads. Setting
  // either answer twice is a no-op, so this cannot loop. The element goes into
  // state as well, so a box that unmounts and comes back (a list switching
  // between a board and a table) is re-watched rather than leaving the observer
  // below holding a node that is no longer on the page.
  useLayoutEffect(() => {
    setScrolls(overflowsSideways(box.current));
    setWatched(box.current);
  });
  // A window resize is only one of the ways the box changes size, and the least
  // interesting one: the sidebar collapsing, a rail opening beside the table,
  // a settings card that is 720px on one route and full width on the next all
  // move the edge without the window moving at all. So the BOX is watched, and
  // the table inside it too — a table that grew is the other half of the same
  // question.
  useEffect(() => {
    // Measured once wherever the observer is unavailable (jsdom): the answer is
    // still right for the render that just happened, it simply stops following
    // a resize.
    if (!watched || typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver(() =>
      setScrolls(overflowsSideways(watched)),
    );
    observer.observe(watched);
    const content = watched.firstElementChild;
    if (content) {
      observer.observe(content);
    }
    return () => observer.disconnect();
  }, [watched]);
  return scrolls ? { tabIndex: 0, role: "region", "aria-label": label } : {};
}

/**
 * The box a table too wide for its column scrolls sideways INSIDE.
 *
 * The one spelling of `.table-scroll`. A settings page is 720px wide and a
 * record's finance table is eight columns, so the overflow is a property of the
 * TABLE rather than a knob each page answers for — and the four screens that
 * had each written this wrapper by hand were four chances to forget the part
 * below.
 *
 * Reachability is `useScrollRegion`'s, above: the tab stop and the name arrive
 * only while the box is actually holding something past its right edge.
 *
 * `label` is what the region is called ("Recent invoices", "Spend by task") and
 * is the caller's to translate. It is required rather than defaulted because a
 * region announced as "region" tells a reader nothing about which of the page's
 * tables they have just landed in.
 */
export function TableScroll({
  label,
  className,
  children,
}: Readonly<{ label: string; className?: string; children: ReactNode }>) {
  const box = useRef<HTMLDivElement | null>(null);
  const region = useScrollRegion(box, label);
  return (
    <div
      ref={box}
      className={["table-scroll", className ?? ""].filter(Boolean).join(" ")}
      {...region}
    >
      {children}
    </div>
  );
}

export function DataTable<Row>({
  columns,
  rows,
  rowKey,
  onRowClick,
  label,
}: Readonly<{
  columns: { key: string; header: string; render: (row: Row) => ReactNode }[];
  rows: Row[];
  rowKey: (row: Row) => string;
  onRowClick?: (row: Row) => void;
  /** What the scroll region is called once the table is wider than its box. */
  label: string;
}>) {
  return (
    <TableScroll label={label}>
      <table className="table">
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column.key}>{column.header}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr
              key={rowKey(row)}
              className={onRowClick ? "rowlink" : undefined}
              onClick={onRowClick ? () => onRowClick(row) : undefined}
            >
              {columns.map((column) => (
                <td key={column.key}>{column.render(row)}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </TableScroll>
  );
}

/**
 * Disclosure is a section the reader opens when they want it.
 *
 * For the surfaces a record page carries but does not lead with — one-off
 * tools, configuration, the occasional deep read. Kept as a standing card
 * each of those competes for the eye with the facts a reader came for; kept
 * behind a summary they cost one line until asked for.
 *
 * `open` forces it open for a state the reader must not miss (a tool that is
 * running, a result that just arrived); left undefined the reader decides.
 *
 * `summary` is a node rather than a string because a summary is a ROW, and
 * some of them carry more than a label — a count beside the name, a status
 * chip. Passing a string stays the ordinary case and reads identically; the
 * alternative was a second `<details>` implementation living beside this one,
 * which is how two disclosures on one screen end up disagreeing about their
 * own caret. `className` is the same bargain for the row's chrome.
 */
// OverflowMenu folds the verbs a record offers but a reader rarely wants —
// merge, archive, share — behind one control, so the header carries identity
// and the frequent actions rather than a row of buttons of equal weight where
// the destructive ones sit next to the routine ones.
//
// The children are the caller's own action components (each opening its own
// confirm flow), so the menu owns only the disclosure: it closes on Escape and
// on a click outside. It deliberately stays open when an item is clicked —
// that item's dialog restores focus to whatever opened it, so hiding it would
// send focus, on close, to a node that is gone.
//
// The children are not rendered until the menu is first opened. They are
// components with their own reads — the company's edit form alone fetches the
// user roster and the custom-field catalogue — and every reader of every
// record page was paying for them without ever opening the menu. Once opened
// they STAY mounted, so a dialog survives the panel being hidden again.
//
// The PANEL is portalled to the body and positioned against the trigger. A
// menu that opened inside its own container was clipped by whatever that
// container clips — a Panel hides its overflow so full-bleed rows respect its
// radius — and a row near the bottom edge of a card lost the actions the menu
// exists to offer. Positioning against the trigger keeps it under the button
// it belongs to wherever that button has moved to.
export function OverflowMenu({
  label,
  children,
}: Readonly<{
  label: string;
  children: ReactNode;
}>) {
  const [open, setOpen] = useState(false);
  const [everOpened, setEverOpened] = useState(false);
  const wrap = useRef<HTMLDivElement | null>(null);
  const panel = useRef<HTMLDivElement | null>(null);
  const trigger = useRef<HTMLButtonElement | null>(null);
  const panelId = useId();
  const at = useAnchoredToTrigger(open, trigger, panel);

  useEffect(() => {
    if (!open) {
      return;
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key !== "Escape") {
        return;
      }
      // A dialog opened from this menu owns Escape while it is up. Closing
      // both layers on one keypress would take the reader back past the menu
      // they were choosing from, and they would have to reopen it to pick
      // something else.
      if (document.querySelector(".overlay")) {
        return;
      }
      setOpen(false);
      trigger.current?.focus();
    };
    const onPointer = (event: MouseEvent) => {
      if (!(event.target instanceof Node)) {
        return;
      }
      // A dialog this menu opened is portalled to the body, so every click
      // inside it looks like a click outside the menu. Closing on those would
      // hide the item the dialog has to give focus back to when it closes.
      if (event.target instanceof Element && event.target.closest(".overlay")) {
        return;
      }
      // The panel lives at the body, not inside the wrapper, so "outside" is
      // outside BOTH — without the second test every click on an item would
      // read as a click away from the menu.
      if (
        !wrap.current?.contains(event.target) &&
        !panel.current?.contains(event.target)
      ) {
        setOpen(false);
      }
    };
    globalThis.addEventListener("keydown", onKey);
    globalThis.addEventListener("mousedown", onPointer);
    return () => {
      globalThis.removeEventListener("keydown", onKey);
      globalThis.removeEventListener("mousedown", onPointer);
    };
  }, [open]);

  return (
    <div className="overflow-menu" ref={wrap}>
      {/* A disclosure, not an ARIA menu. `role="menu"` promises arrow-key
          navigation and a roving tabstop; the items here are the caller's own
          buttons, each opening its own dialog, and Tab through them is the
          behaviour a reader actually gets. Announcing a menu we do not
          implement is worse than announcing the expandable region we do. */}
      <button
        type="button"
        ref={trigger}
        className="btn btn-ghost btn-sm overflow-menu-trigger"
        aria-expanded={open}
        aria-controls={panelId}
        aria-label={label}
        title={label}
        onClick={() => {
          setEverOpened(true);
          setOpen((was) => !was);
        }}
      >
        <MoreHorizontal aria-hidden="true" size={16} />
      </button>
      {/* Hidden, never unmounted. The items own their own dialogs, so
          unmounting them on close would throw away the dialog the click just
          opened. `hidden` also takes them out of the tab order, so a closed
          menu is closed for a keyboard reader too.

          Clicking an item does NOT close the panel. The item opens a dialog
          that covers the page, and a dialog restores focus to whatever opened
          it — so hiding that item first would send focus, on close, to a node
          that is no longer there. Leaving the panel open keeps the return
          target visible; the outside-click below then closes it on the
          reader's next move. */}
      {createPortal(
        <div
          id={panelId}
          ref={panel}
          className="overflow-menu-items"
          hidden={!open}
          style={{
            top: `${at.top}px`,
            left: `${at.left}px`,
            maxHeight: `${at.maxHeight}px`,
          }}
        >
          {everOpened && children}
        </div>,
        document.body,
      )}
    </div>
  );
}

// Where the portalled panel sits: beside the trigger, right edge to right edge,
// and INSIDE the viewport on both axes.
//
// The panel is fixed, so the viewport is all the room there is — a panel placed
// below a trigger near the bottom edge puts its actions where no amount of page
// scrolling reaches them. So it opens upward when the space below cannot hold
// it, takes whichever side has more room when neither can, and is capped to
// that space so a panel with many items scrolls inside itself.
//
// Measured on OPEN and again whenever anything moves it. Scroll is listened to
// in the CAPTURE phase because a scroll event does not bubble — the trigger may
// sit inside a scrolling region, and a panel that stayed at the coordinates it
// was opened at would drift away from the button it belongs to.
function useAnchoredToTrigger(
  open: boolean,
  trigger: RefObject<HTMLButtonElement | null>,
  panel: RefObject<HTMLDivElement | null>,
): { top: number; left: number; maxHeight: number } {
  const [at, setAt] = useState({ top: 0, left: 0, maxHeight: 0 });
  useEffect(() => {
    if (!open) {
      return;
    }
    const place = () => {
      const anchor = trigger.current?.getBoundingClientRect();
      if (!anchor) {
        return;
      }
      const width = panel.current?.offsetWidth ?? 0;
      const room = globalThis.innerWidth - width - MENU_EDGE_GAP;
      setAt({
        ...verticalPlacement(anchor, panel.current?.offsetHeight ?? 0),
        left: Math.max(MENU_EDGE_GAP, Math.min(anchor.right - width, room)),
      });
    };
    place();
    globalThis.addEventListener("resize", place);
    globalThis.addEventListener("scroll", place, true);
    return () => {
      globalThis.removeEventListener("resize", place);
      globalThis.removeEventListener("scroll", place, true);
    };
  }, [open, trigger, panel]);
  return at;
}

// Below the trigger while the panel fits there, above it when it does not, and
// on the roomier side when neither fits — capped to that side either way.
//
// Exported for its own test: jsdom gives every element a zero-sized rectangle,
// so the only way to state this rule as a test is to state it over the
// measurements themselves.
export function verticalPlacement(
  anchor: DOMRect,
  height: number,
): { top: number; maxHeight: number } {
  const below = globalThis.innerHeight - anchor.bottom - MENU_EDGE_GAP * 2;
  const above = anchor.top - MENU_EDGE_GAP * 2;
  const opensDown = height <= below || below >= above;
  if (opensDown) {
    return { top: anchor.bottom + MENU_EDGE_GAP, maxHeight: below };
  }
  return {
    top: Math.max(MENU_EDGE_GAP, anchor.top - MENU_EDGE_GAP - height),
    maxHeight: above,
  };
}

// The breathing room between the panel and both the trigger above it and the
// viewport edge beside it, in px because it is arithmetic rather than a
// stylesheet value: --space-1.
const MENU_EDGE_GAP = 4;

export function Disclosure({
  summary,
  open,
  className,
  children,
}: Readonly<{
  summary: ReactNode;
  open?: boolean;
  className?: string;
  children: ReactNode;
}>) {
  return (
    <details
      className={className ? `disclosure ${className}` : "disclosure"}
      open={open}
    >
      <summary className="disclosure-summary">
        <ChevronRight className="disclosure-chevron" aria-hidden="true" />
        <span className="t-label">{summary}</span>
      </summary>
      <div className="disclosure-body">{children}</div>
    </details>
  );
}
