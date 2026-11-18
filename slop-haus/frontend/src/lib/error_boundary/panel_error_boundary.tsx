import { Component, useContext, type ErrorInfo, type ReactNode } from "react";
import { Button } from "@slop-factory/ui";
import { ChunkLoadFallback, ErrorReporterContext, isChunkLoadError } from "@slop-factory/logging/error_reporting/react";
import type { ErrorReporter } from "@slop-factory/logging/error_reporting";

interface Props {
  // Names the panel in the log and in the fallback.
  panel: string;
  reporter: ErrorReporter | null;
  children: ReactNode;
}

interface State {
  failed: boolean;
  error?: unknown;
}

class Boundary extends Component<Props, State> {
  state: State = { failed: false };

  static getDerivedStateFromError(error: unknown): State {
    return { failed: true, error };
  }

  // Through the reporter, which redacts the message: a render error can
  // carry what the panel was showing.
  componentDidCatch(error: unknown, info: ErrorInfo) {
    // A failed chunk is reported by its fallback, as its own kind.
    if (isChunkLoadError(error)) return;
    this.props.reporter?.reportError("render error", error, info.componentStack ?? undefined);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    // "Try again" would fetch the same missing chunk: reload instead.
    if (isChunkLoadError(this.state.error)) return <ChunkLoadFallback reporter={this.props.reporter} error={this.state.error} />;
    return (
      <div role="alert">
        <p>The {this.props.panel} could not be shown.</p>
        <Button size="sm" onClick={() => this.setState({ failed: false, error: undefined })}>
          Try again
        </Button>
      </div>
    );
  }
}

// PanelErrorBoundary keeps a render error in one independent panel from
// taking down the route: the rest of the page stays usable and the panel
// offers a retry. Reads the reporter optionally so a story needs no provider.
export function PanelErrorBoundary({ panel, children }: { panel: string; children: ReactNode }) {
  const reporter = useContext(ErrorReporterContext);
  return (
    <Boundary panel={panel} reporter={reporter}>
      {children}
    </Boundary>
  );
}
