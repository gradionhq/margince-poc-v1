/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { LocaleProvider } from "../../i18n";
import type { ThreadEntry } from "./conversation-machine";
import { entityQuestion } from "./test-fixtures";
import { ConversationThread } from "./thread";

// The thread's presentation duties: live narration reveals word by word
// while restored entries render instantly, and the one live question card
// claims keyboard focus only when no text field has a better claim.

afterEach(cleanup);

const restoredNarration: ThreadEntry = {
  kind: "narration",
  id: "0:recap",
  i18nKey: "ob.conv.recap.back",
};

const liveNarration: ThreadEntry = {
  kind: "narration",
  id: "1:read:started",
  i18nKey: "ob.conv.read.started",
  params: { host: "gradion.com" },
};

const questionEntry: ThreadEntry = {
  kind: "question",
  id: "2:question:clarify-entity",
  question: entityQuestion,
};

type ThreadProps = Readonly<{
  entries: readonly ThreadEntry[];
  pendingQuestionId?: string | null;
  composerValue?: string;
}>;

// The composer guard walks the DOM the workbench provides: the panel class
// around the thread and the composer beside it.
function Harness({
  entries,
  pendingQuestionId = null,
  composerValue = "",
}: ThreadProps) {
  return (
    <LocaleProvider initial="en">
      <section className="ob-workbench-panel">
        <ConversationThread
          entries={entries}
          pendingQuestionId={pendingQuestionId}
          onAnswer={() => {}}
        />
        <div className="mw-composer">
          <textarea aria-label="composer" defaultValue={composerValue} />
        </div>
      </section>
    </LocaleProvider>
  );
}

describe("word-by-word reveal", () => {
  it("renders entries present at mount instantly, without reveal markup", () => {
    const { container } = render(<Harness entries={[restoredNarration]} />);
    expect(container.querySelector(".ob-conv-reveal")).toBeNull();
    expect(screen.getByText(/Welcome back\./)).toBeTruthy();
  });

  it("reveals narration that arrives after mount, keeping the full sentence readable", () => {
    const { container, rerender } = render(
      <Harness entries={[restoredNarration]} />,
    );
    rerender(<Harness entries={[restoredNarration, liveNarration]} />);
    const reveal = container.querySelector(".ob-conv-reveal");
    expect(reveal).not.toBeNull();
    // The animated copy is presentation only; the coherent sentence is the
    // visually hidden source next to it.
    expect(reveal?.getAttribute("aria-hidden")).toBe("true");
    expect(screen.getByText(/Reading gradion\.com now/)).toBeTruthy();
    // The restored entry stays plain.
    const bubbles = container.querySelectorAll(".ob-conv-narration");
    expect(bubbles[0]?.querySelector(".ob-conv-reveal")).toBeNull();
  });
});

describe("live question focus", () => {
  it("moves focus to the live question's first option when the composer is idle", () => {
    render(
      <Harness
        entries={[questionEntry]}
        pendingQuestionId={entityQuestion.id}
      />,
    );
    const first = screen.getByRole("button", { name: "Acme GmbH" });
    expect(document.activeElement).toBe(first);
  });

  it("leaves focus alone while the composer holds a draft", () => {
    render(
      <Harness
        entries={[questionEntry]}
        pendingQuestionId={entityQuestion.id}
        composerValue="https://gradion"
      />,
    );
    const first = screen.getByRole("button", { name: "Acme GmbH" });
    expect(document.activeElement).not.toBe(first);
  });

  it("leaves focus alone while a text field is focused", () => {
    const { rerender } = render(<Harness entries={[]} />);
    screen.getByLabelText("composer").focus();
    rerender(
      <Harness
        entries={[questionEntry]}
        pendingQuestionId={entityQuestion.id}
      />,
    );
    expect(document.activeElement).toBe(screen.getByLabelText("composer"));
  });

  it("never focuses an answered card", () => {
    render(<Harness entries={[questionEntry]} pendingQuestionId={null} />);
    const first = screen.getByRole("button", { name: "Acme GmbH" });
    expect(document.activeElement).not.toBe(first);
  });
});
