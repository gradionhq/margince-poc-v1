import { useEffect, useRef } from "react";
import type { Warning } from "./types";
import "./view.css";

/**
 * The wrapper the renderer stories mount the REAL render() into. One wrapper for
 * both views, taking the renderer as a prop, because two copies of twelve lines
 * is two places for the mount to drift from what the document does.
 *
 * A CAVEAT THAT BELONGS IN THE FILE, not in a review comment: .storybook/
 * preview.tsx imports app.css, so these stories render against ambient Tailwind
 * and base element styles the standalone document does NOT have. A rule that
 * only works because app.css was loaded looks correct here and breaks inside a
 * host — the document stories beside these are what catch that.
 */
export function ViewHost({
  render,
  data,
  warnings = [],
}: {
  render: (root: HTMLElement, data: unknown, warnings: Warning[]) => void;
  data: unknown;
  warnings?: Warning[];
}) {
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    if (ref.current !== null) render(ref.current, data, warnings);
  }, [render, data, warnings]);
  return <main ref={ref} />;
}
