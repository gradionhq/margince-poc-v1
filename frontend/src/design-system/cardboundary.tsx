// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { QueryErrorResetBoundary } from "@tanstack/react-query";
import { Component, type ReactNode } from "react";
import { useT } from "../i18n";
import { Button, EmptyState } from "./atoms";

// CardBoundary contains ONE card's render failure inside that card.
//
// The app-level boundary (app/errorboundary.tsx) is the floor, not the whole
// story: it catches the throw, but by then the whole routed shell has unmounted
// and the reader is looking at a single apology where a page used to be — the
// navigation rail included, so the one thing they could have done next (go
// somewhere else) went with it. A page assembled from a dozen independent reads
// has a dozen ways to throw, and every one of them costs the reader the page.
//
// This boundary keeps its own place. The card that failed says so and offers a
// retry; the eleven beside it, and the rail, never learn anything happened.
//
// It never shows the thrown error's text, for the same reason the app-level one
// does not: a render throw is a defect in this app, not a message from the
// server. Its text names our internals, the reader cannot act on it, and a
// stack trace on a settings page is the app leaking rather than explaining.

function CardFailure({
  title,
  retryLabel,
  onRetry,
}: Readonly<{ title: string; retryLabel: string; onRetry: () => void }>) {
  return (
    // role="alert" so the failure is announced where it happened — a reader
    // who is elsewhere on the page when a card gives out is told, without the
    // page stealing their focus to do it. EmptyState draws the recessed card
    // itself, so the boundary keeps the failed card's PLACE in the layout: the
    // grid does not reflow around a hole, which is what a returned null would
    // have made it do.
    <div role="alert">
      <EmptyState>
        <p>{title}</p>
        <Button
          variant="primary"
          small
          onClick={onRetry}
          style={{ marginTop: "var(--space-3)" }}
        >
          {retryLabel}
        </Button>
      </EmptyState>
    </div>
  );
}

type BoundaryProps = Readonly<{
  onReset: () => void;
  title: string;
  retryLabel: string;
  children: ReactNode;
}>;
type BoundaryState = Readonly<{ failed: boolean }>;

// React reports a caught error to the console with its component stack on its
// own, so this boundary adds no reporting of its own — the same failure logged
// twice teaches a reader to skim both.
class CardRenderBoundary extends Component<BoundaryProps, BoundaryState> {
  state: BoundaryState = { failed: false };

  static getDerivedStateFromError(): BoundaryState {
    return { failed: true };
  }

  private readonly retry = () => {
    // The cache is reset BEFORE the re-render: a query that threw keeps its
    // failed result, so a boundary that only cleared its own state would hand
    // the same error straight back and the button would do nothing visible.
    this.props.onReset();
    this.setState({ failed: false });
  };

  render(): ReactNode {
    if (this.state.failed) {
      return (
        <CardFailure
          title={this.props.title}
          retryLabel={this.props.retryLabel}
          onRetry={this.retry}
        />
      );
    }
    return this.props.children;
  }
}

/**
 * Wrap ONE card. Paired with the query cache's own reset boundary so the retry
 * clears the failures the cache is holding as well as this boundary's state.
 *
 * The copy is the one exception to "a primitive holds no copy" that this file
 * cannot avoid and does not try to hide: the fallback renders after the card
 * that would have passed the words has already thrown, so it reads the two
 * `card.error*` keys itself rather than taking props that may never arrive.
 * They say nothing about which card this is — the card's own place on the page
 * already does.
 */
export function CardBoundary({ children }: Readonly<{ children: ReactNode }>) {
  const t = useT();
  return (
    <QueryErrorResetBoundary>
      {({ reset }) => (
        <CardRenderBoundary
          onReset={reset}
          title={t("card.errorTitle")}
          retryLabel={t("card.errorRetry")}
        >
          {children}
        </CardRenderBoundary>
      )}
    </QueryErrorResetBoundary>
  );
}
