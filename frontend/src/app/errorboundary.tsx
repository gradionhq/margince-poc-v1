import { QueryErrorResetBoundary } from "@tanstack/react-query";
import { Component, type ReactNode } from "react";
import { Button, EmptyState } from "../design-system/atoms";
import { useT } from "../i18n";

// The app-level error boundary (architecture/frontend, "the data layer").
// Without one, a single throw during render unmounts the whole tree and the
// reader is left on a white page with no way back short of a reload they have
// to think of themselves. With one, the throw costs them the view and nothing
// else.

// The fallback never shows the thrown error. A render throw is a defect in
// this app, not a message from the server: its text names our internals, and
// the reader cannot act on it. What they can act on is here.
function RenderFailure({ onRetry }: Readonly<{ onRetry: () => void }>) {
  const t = useT();
  return (
    <div className="wrap narrow" role="alert">
      <EmptyState>
        <p>{t("app.errorTitle")}</p>
        <p className="card-sub">{t("app.errorBody")}</p>
        <Button
          variant="primary"
          small
          onClick={onRetry}
          style={{ marginTop: 10 }}
        >
          {t("app.errorRetry")}
        </Button>
      </EmptyState>
    </div>
  );
}

type BoundaryProps = Readonly<{ onReset: () => void; children: ReactNode }>;
type BoundaryState = Readonly<{ failed: boolean }>;

// React reports a caught error to the console with its component stack on its
// own, so this boundary adds no reporting of its own — the same failure logged
// twice teaches a reader to skim both.
class RenderErrorBoundary extends Component<BoundaryProps, BoundaryState> {
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
      return <RenderFailure onRetry={this.retry} />;
    }
    return this.props.children;
  }
}

// Wraps the routed shell, paired with the query cache's reset boundary so the
// retry clears the failures the cache is holding as well as the boundary's own
// state.
export function AppErrorBoundary({
  children,
}: Readonly<{ children: ReactNode }>) {
  return (
    <QueryErrorResetBoundary>
      {({ reset }) => (
        <RenderErrorBoundary onReset={reset}>{children}</RenderErrorBoundary>
      )}
    </QueryErrorResetBoundary>
  );
}
