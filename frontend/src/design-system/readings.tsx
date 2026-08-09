import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import "./readings.css";

// Three ways to draw a reading that is not a number in a box: a proportion as
// a bar, a series as a line, and a labelled attribute as a pill. Copy always
// arrives through props — these never hard-code a word.

// A proportion, drawn as a bar. `value` and `max` are the two halves of the
// same fact (7 of 9 inputs present), so the caller passes both rather than a
// pre-computed percentage: the bar and the label a caller writes beside it
// then cannot disagree, and a zero max reads as an empty bar instead of NaN.
export function Meter({
  value,
  max,
  label,
  tone,
}: Readonly<{
  value: number;
  max: number;
  label: string;
  // The bar carries the accent gradient by default. "warn"/"danger" are for a
  // reading whose LOW end is the bad one — coverage, payment behaviour.
  tone?: "warn" | "danger";
}>) {
  const filled = max > 0 ? Math.min(100, Math.max(0, (value / max) * 100)) : 0;
  // Not a native <meter>: its children are fallback content that a supporting
  // engine never renders, so the token-drawn fill below would simply vanish,
  // and the bar every engine draws in its place takes none of our colours —
  // the same reason `<select>` is banned outright. The role and the value
  // triple carry the reading to assistive tech instead.
  return (
    // biome-ignore lint/a11y/useSemanticElements: <meter> discards the token-drawn fill and draws its own untokenised bar
    <div
      className={tone ? `meterbar meterbar-${tone}` : "meterbar"}
      role="meter"
      aria-label={label}
      aria-valuenow={value}
      aria-valuemin={0}
      aria-valuemax={max}
    >
      <span style={{ width: `${filled}%` }} />
    </div>
  );
}

// A short series as a bare polyline: the shape of a trend, with no axes, no
// grid and no tooltip. It is a glyph, not a chart — a reader who needs the
// figures reads the table beside it, which is why the whole thing is
// aria-hidden behind the caller's own text label.
//
// Fewer than two points draws nothing: a single point is a dot the reader
// would read as a flat trend, which is a claim the data does not make.
export function Sparkline({
  points,
  label,
}: Readonly<{ points: readonly number[]; label: string }>) {
  if (points.length < 2) {
    return null;
  }
  const low = Math.min(...points);
  const high = Math.max(...points);
  // A flat series has no range to scale into; drawing it down the middle says
  // "unchanged", which is exactly what it is.
  const span = high - low || 1;
  const step = SPARK_WIDTH / (points.length - 1);
  const path = points
    .map((point, index) => {
      const x = (index * step).toFixed(1);
      const y = (
        SPARK_HEIGHT -
        ((point - low) / span) * (SPARK_HEIGHT - SPARK_INSET * 2) -
        SPARK_INSET
      ).toFixed(1);
      return `${x},${y}`;
    })
    .join(" ");
  return (
    <svg
      className="sparkline"
      viewBox={`0 0 ${SPARK_WIDTH} ${SPARK_HEIGHT}`}
      preserveAspectRatio="none"
      role="img"
      aria-label={label}
    >
      <polyline points={path} />
    </svg>
  );
}

const SPARK_WIDTH = 120;
const SPARK_HEIGHT = 32;
// Keeps the stroke off the top and bottom edges, where a 2px line drawn at
// the extreme would be clipped in half by the viewBox.
const SPARK_INSET = 3;

// An attribute of a record with the icon that names its kind: the website, the
// LinkedIn page, the location, the industry, the headcount. Distinct from
// `Badge`, which is a status in a closed tone vocabulary — a Chip is a fact,
// and it is a link when the fact has somewhere to go.
export function Chip({
  icon: Icon,
  children,
  href,
}: Readonly<{
  icon: LucideIcon;
  children: ReactNode;
  // An external destination. Present → the chip is an anchor and opens in a
  // new tab with `noreferrer`, since these point off our origin.
  href?: string;
}>) {
  const body = (
    <>
      <Icon size={14} aria-hidden="true" />
      <span>{children}</span>
    </>
  );
  if (href) {
    return (
      <a
        className="chip chip-link"
        href={href}
        target="_blank"
        rel="noreferrer"
      >
        {body}
      </a>
    );
  }
  return <span className="chip">{body}</span>;
}
