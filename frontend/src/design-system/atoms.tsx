import { Search } from "lucide-react";
import {
  type ButtonHTMLAttributes,
  type InputHTMLAttributes,
  type ReactNode,
  useEffect,
} from "react";
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

export function Avatar({ name }: Readonly<{ name: string }>) {
  const initials = name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase())
    .join("");
  return <span className="avatar">{initials}</span>;
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

export function Card({
  inset,
  children,
  className,
}: Readonly<{
  inset?: boolean;
  children: ReactNode;
  className?: string;
}>) {
  return (
    <div
      className={["card", inset ? "card-inset" : "", className ?? ""]
        .filter(Boolean)
        .join(" ")}
    >
      {children}
    </div>
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
  return <div className="card card-inset empty">{children}</div>;
}

export function SectionHeader({
  title,
  sub,
}: Readonly<{ title: string; sub?: string }>) {
  return (
    <div className="section-header">
      <h2>{title}</h2>
      {sub && <span className="sub">{sub}</span>}
    </div>
  );
}

export function SegmentedControl<Option extends string>({
  options,
  value,
  onChange,
  labels,
}: Readonly<{
  options: readonly Option[];
  value: Option;
  onChange: (next: Option) => void;
  labels: Record<Option, string>;
}>) {
  return (
    <fieldset className="segmented">
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
  children,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  labelledBy: string;
  // "wide" roomier variant for content-dense dialogs (code/YAML previews);
  // "default" keeps the compact form width every confirm/create modal uses.
  size?: "default" | "wide";
  children: ReactNode;
}>) {
  useEffect(() => {
    if (!open) {
      return;
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        onClose();
      }
    };
    globalThis.addEventListener("keydown", onKey);
    return () => globalThis.removeEventListener("keydown", onKey);
  }, [open, onClose]);
  if (!open) {
    return null;
  }
  return (
    // NOSONAR: backdrop dismiss only; keyboard path (Esc) handled by the effect above
    // biome-ignore lint/a11y/noStaticElementInteractions: backdrop dismiss is a convention; Esc is the keyboard path
    // biome-ignore lint/a11y/useKeyWithClickEvents: Esc handles the keyboard path above
    <div
      className="overlay"
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
        className={size === "wide" ? "modal modal-wide" : "modal"}
      >
        {children}
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
