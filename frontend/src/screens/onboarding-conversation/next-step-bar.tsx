import { useEffect, useState } from "react";
import "./conversation.css";

// The pinned next-step bar between the thread and the composer: whenever
// the current act has a blocking affordance (an open decision, the ready
// review, the enabled build chip) that sits scrolled out of view, one slim
// line names it and one click brings it back. The bar disappears the moment
// the affordance itself is visible — it points at the next step, it never
// duplicates it. It lives OUTSIDE the thread's log region on purpose: a
// status wrapper nested in the live log would be announced twice.

type NextStepBarProps = Readonly<{
  /** Short factual line, already translated; doubles as the button label. */
  label: string;
  /** Where the affordance lives in the document; re-resolved per render
   * cycle because the card mounts in the same commit as the bar. */
  targetSelector: string;
  /** A counter that moves when the thread changes (the machine's entry
   * seq), so the target re-resolves after cards mount or retire. */
  revision: number;
}>;

const FOCUSABLE =
  "button:not([disabled]), [href], input:not([disabled]), textarea, select";

function scrollToTarget(target: HTMLElement): void {
  const reduceMotion =
    globalThis.matchMedia?.("(prefers-reduced-motion: reduce)").matches ??
    false;
  // jsdom has no scrollIntoView; in the browser it always exists.
  target.scrollIntoView?.({
    block: "center",
    behavior: reduceMotion ? "auto" : "smooth",
  });
  const focusable = target.matches(FOCUSABLE)
    ? target
    : target.querySelector<HTMLElement>(FOCUSABLE);
  focusable?.focus();
}

export function NextStepBar({
  label,
  targetSelector,
  revision,
}: NextStepBarProps) {
  const [target, setTarget] = useState<HTMLElement | null>(null);
  const [targetInView, setTargetInView] = useState(false);

  // biome-ignore lint/correctness/useExhaustiveDependencies: revision is a deliberate re-resolution trigger — the card can mount a commit after the bar under the same selector
  useEffect(() => {
    const found = document.querySelector<HTMLElement>(targetSelector);
    setTarget(found);
    // Without observer support the bar stays visible: for a discoverability
    // aid, pointing at an already-visible card beats hiding a missed one.
    if (found === null || typeof IntersectionObserver === "undefined") {
      setTargetInView(false);
      return;
    }
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          setTargetInView(entry.isIntersecting);
        }
      },
      // Root: the thread scroll container the card lives in (viewport when
      // detached); half-visible still counts as needing the pointer.
      { root: found.closest(".ob-conv-thread"), threshold: 0.5 },
    );
    observer.observe(found);
    return () => observer.disconnect();
  }, [targetSelector, revision]);

  if (target === null || targetInView) {
    return null;
  }
  return (
    <div role="status" className="ob-conv-nextstep">
      <button type="button" onClick={() => scrollToTarget(target)}>
        {label}
      </button>
    </div>
  );
}
