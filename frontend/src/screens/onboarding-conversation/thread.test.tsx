/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../../i18n";
import type { ThreadEntry } from "./conversation-machine";
import { entityQuestion } from "./test-fixtures";
import { ConversationThread } from "./thread";

// The thread's presentation duties: live narration reveals word by word
// while restored entries render instantly, the one live question card
// claims keyboard focus only when no text field has a better claim, and a
// question card is interactive IF AND ONLY IF it is the machine's current
// pending question instance — every other card is fully inert, with its
// recorded choice shown.

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
  onAnswer?: (questionId: string, value: string) => void;
  onDismiss?: (questionId: string) => void;
}>;

// The composer guard walks the DOM the workbench provides: the panel class
// around the thread and the composer beside it.
function Harness({
  entries,
  pendingQuestionId = null,
  composerValue = "",
  onAnswer = () => {},
  onDismiss,
}: ThreadProps) {
  return (
    <LocaleProvider initial="en">
      <section className="ob-workbench-panel">
        <ConversationThread
          entries={entries}
          pendingQuestionId={pendingQuestionId}
          onAnswer={onAnswer}
          onDismiss={onDismiss}
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

describe("question card interactivity", () => {
  const dismissibleQuestion = {
    ...entityQuestion,
    dismissLabelKey: "ob.conv.clarify.dismiss" as const,
  };
  const dismissibleEntry: ThreadEntry = {
    kind: "question",
    id: "2:question:clarify-entity",
    question: dismissibleQuestion,
  };

  it("a card that is no longer pending is fully inert: every control disabled, clicks change nothing", async () => {
    const onAnswer = vi.fn();
    const onDismiss = vi.fn();
    // The machine advanced (a dismissal or answer cleared the pending
    // question); the card's moment passed even though no answer turn for it
    // exists in the thread.
    render(
      <Harness
        entries={[dismissibleEntry]}
        pendingQuestionId={null}
        onAnswer={onAnswer}
        onDismiss={onDismiss}
      />,
    );

    const option = screen.getByRole("button", {
      name: "Acme GmbH",
    }) as HTMLButtonElement;
    const dismiss = screen.getByRole("button", {
      name: "Skip this - I will set it myself",
    }) as HTMLButtonElement;
    expect(option.disabled).toBe(true);
    expect(dismiss.disabled).toBe(true);
    await userEvent.click(option);
    await userEvent.click(dismiss);
    expect(onAnswer).not.toHaveBeenCalled();
    expect(onDismiss).not.toHaveBeenCalled();
  });

  it("the live pending card stays interactive and answering works", async () => {
    const onAnswer = vi.fn();
    render(
      <Harness
        entries={[dismissibleEntry]}
        pendingQuestionId={entityQuestion.id}
        onAnswer={onAnswer}
      />,
    );

    const option = screen.getByRole("button", {
      name: "Acme GmbH",
    }) as HTMLButtonElement;
    expect(option.disabled).toBe(false);
    await userEvent.click(option);
    expect(onAnswer).toHaveBeenCalledWith(entityQuestion.id, "acme-gmbh");
  });

  it("a resolved card shows the recorded option choice", () => {
    const answerTurn: ThreadEntry = {
      kind: "user",
      id: "3:answer:clarify-entity",
      text: "Acme GmbH",
    };
    render(
      <Harness
        entries={[questionEntry, answerTurn]}
        pendingQuestionId={null}
      />,
    );

    const chosen = screen.getAllByRole("button", { name: "Acme GmbH" })[0];
    expect(chosen.getAttribute("aria-pressed")).toBe("true");
    expect(chosen.className).toContain("ob-conv-option-selected");
    const other = screen.getByRole("button", { name: "Acme Holding SE" });
    expect(other.getAttribute("aria-pressed")).toBe("false");
  });

  it("a dismissed card marks the dismissal as its recorded choice", () => {
    const dismissTurn: ThreadEntry = {
      kind: "user",
      id: "3:answer:clarify-entity",
      i18nKey: "ob.conv.clarify.dismiss",
    };
    render(
      <Harness
        entries={[dismissibleEntry, dismissTurn]}
        pendingQuestionId={null}
        onDismiss={() => {}}
      />,
    );

    const dismiss = screen.getByRole("button", {
      name: "Skip this - I will set it myself",
    }) as HTMLButtonElement;
    expect(dismiss.disabled).toBe(true);
    expect(dismiss.className).toContain("ob-conv-option-selected");
  });
});
