import { ChevronRight, MoreHorizontal, Search } from "lucide-react";
import {
  type ButtonHTMLAttributes,
  type CSSProperties,
  type ElementType,
  type FormEventHandler,
  type InputHTMLAttributes,
  type ReactNode,
  type TextareaHTMLAttributes,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import "./atoms.css";

// The Margince atom library (B-EP09.2, re-scoped to our own
// system, no gw-ui port; atoms are added as screens need them). Copy always
// arrives through props — callers translate with t(); atoms never hard-code
// user-facing words.

type ButtonVariant = "primary" | "ghost" | "danger";

export function Button({
  variant = "ghost",
  small,
  className,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant;
  small?: boolean;
}) {
  const classes = [
    "btn",
    `btn-${variant}`,
    small ? "btn-sm" : "",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");
  return <button type="button" className={classes} {...rest} />;
}

export function Badge({
  tone,
  children,
}: Readonly<{
  tone?: "success" | "warn" | "danger" | "ai" | "accent";
  children: ReactNode;
}>) {
  return (
    <span className={tone ? `badge badge-${tone}` : "badge"}>{children}</span>
  );
}

// AVATAR_TONES are the monogram backgrounds, all token-driven. The colour
// is picked from the name, not stored, so the same record looks the same on
// every screen and in every session without a round trip.
const AVATAR_TONES = 6;

export function Avatar({
  name,
  tinted,
  src,
  size,
}: Readonly<{
  name: string;
  // A deterministic per-name colour. Off by default so the many existing
  // callers keep the neutral chip they render today.
  tinted?: boolean;
  // A resolved logo to render instead of the monogram. The monogram is the
  // floor, not the fallback of last resort: it is what shows while the image
  // loads, if it fails to load, and whenever no logo resolved — so a company
  // is never a broken image or an empty slot.
  src?: string | null;
  // "lg" is the record header's larger chip; the default is the 28px chip
  // every list and row uses.
  size?: "lg";
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
  const initials = name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase())
    .join("");
  let tone = 0;
  if (tinted) {
    // A small sum over the code points: stable across sessions and locales,
    // and the spread only has to be even enough that neighbouring names in a
    // list rarely collide.
    let hash = 0;
    for (const char of name) {
      hash = (hash + (char.codePointAt(0) ?? 0)) % AVATAR_TONES;
    }
    tone = hash;
  }
  const classes = ["avatar"];
  if (tinted) classes.push(`avatar-t${tone}`);
  if (size === "lg") classes.push("avatar-lg");
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

export function TextInput(props: InputHTMLAttributes<HTMLInputElement>) {
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
 */
export function Textarea(props: TextareaHTMLAttributes<HTMLTextAreaElement>) {
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
  hint,
  required,
  className,
  children,
}: Readonly<{
  // A node, not a string: a label is usually words, but a field whose value was
  // read from somewhere carries its provenance in the label row — a confidence
  // meter and a source chip beside the name.
  label: ReactNode;
  hint?: string;
  required?: boolean;
  // Layout the surrounding form owns — a width, a grid span, a screen's own
  // field modifier. It lands on the wrapper, which is the only element a
  // caller has any business positioning.
  className?: string;
  children: (control: FieldControl) => ReactNode;
}>) {
  const id = useId();
  const hintId = hint ? `${id}-hint` : undefined;
  return (
    <div className={["field", className ?? ""].filter(Boolean).join(" ")}>
      <label className="t-label" htmlFor={id}>
        {label}
        {required && <span aria-hidden> *</span>}
      </label>
      {children({ id, required, "aria-describedby": hintId })}
      {hint && (
        <p className="t-caption" id={hintId}>
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
 */
export function StatCard({
  label,
  value,
  detail,
  tone,
  source,
}: Readonly<{
  label: string;
  value: string;
  detail?: string;
  tone?: "warn" | "danger";
  // Where the figure came from, named on the card that shows it. A money
  // reading a reader cannot trace is one they have to go and verify
  // elsewhere, which is the trip the badge saves them.
  source?: ReactNode;
}>) {
  return (
    <section className="stat-card">
      <span className="stat-card-label t-caption">
        {label}
        {source && <span className="stat-card-source">{source}</span>}
      </span>
      <span
        className={
          tone
            ? `stat-card-value t-h3 stat-card-${tone}`
            : "stat-card-value t-h3"
        }
      >
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
  children,
  className,
  style,
  id,
  ariaLabel,
  testId,
  onSubmit,
}: Readonly<{
  as?: CardElement;
  inset?: boolean;
  title?: string;
  sub?: string;
  actions?: ReactNode;
  children?: ReactNode;
  className?: string;
  style?: CSSProperties;
  id?: string;
  // Naming the card makes it a region a screen reader can land on and list;
  // spelled out rather than spread so the prop reads the same at every call.
  ariaLabel?: string;
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
      data-testid={testId}
      onSubmit={onSubmit}
    >
      {title !== undefined && (
        <SectionHeader title={title} sub={sub} actions={actions} />
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

export function EmptyState({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <Card as="div" inset className="empty">
      {children}
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
  level?: 1 | 2;
}>) {
  return (
    <div className="section-header">
      <div className="section-header-text">
        {level === 1 ? <h1>{title}</h1> : <h2>{title}</h2>}
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
  children: ReactNode;
}>) {
  const dialog = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
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
      if (opener instanceof HTMLElement) {
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
    // NOSONAR: backdrop dismiss only; keyboard path (Esc) handled by the effect above
    // biome-ignore lint/a11y/noStaticElementInteractions: backdrop dismiss is a convention; Esc is the keyboard path
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc handles the keyboard path above
    <div
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

export function DataTable<Row>({
  columns,
  rows,
  rowKey,
  onRowClick,
}: Readonly<{
  columns: { key: string; header: string; render: (row: Row) => ReactNode }[];
  rows: Row[];
  rowKey: (row: Row) => string;
  onRowClick?: (row: Row) => void;
}>) {
  return (
    <div className="table-scroll">
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
    </div>
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
  const trigger = useRef<HTMLButtonElement | null>(null);
  const panelId = useId();

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
      if (!wrap.current?.contains(event.target)) {
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
      <div id={panelId} className="overflow-menu-items" hidden={!open}>
        {everOpened && children}
      </div>
    </div>
  );
}

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
