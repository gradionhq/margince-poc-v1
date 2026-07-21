import type { ReactNode } from "react";
import "./margince-core.css";

export type MarginceCoreState =
  | "idle"
  | "listening"
  | "working"
  | "success"
  | "attention"
  | "error"
  | "quiet"
  | "unavailable";

export function MarginceCoreScene({
  state = "idle",
  progress,
  children,
  className = "",
}: Readonly<{
  state?: MarginceCoreState;
  progress?: number;
  children?: ReactNode;
  className?: string;
}>) {
  return (
    <div
      className={`margince-core-scene ${children ? "has-content" : ""} ${className}`}
      data-core-state={state}
    >
      {progress !== undefined && (
        <svg
          className="margince-core-progress"
          viewBox="0 0 100 100"
          aria-hidden="true"
        >
          <circle cx="50" cy="50" r="48.5" pathLength="100" />
          <circle
            className="margince-core-progress-value"
            cx="50"
            cy="50"
            r="48.5"
            pathLength="100"
            strokeDasharray={`${Math.max(0, Math.min(1, progress)) * 100} 100`}
          />
        </svg>
      )}
      <div className="margince-core-visual" aria-hidden="true">
        <div className="margince-core-glow" />
        <div className="margince-core-orbit margince-core-orbit-context">
          <span className="margince-core-node margince-core-node-a" />
          <span className="margince-core-node margince-core-node-b" />
        </div>
        <div className="margince-core-orbit margince-core-orbit-evidence">
          <span className="margince-core-node margince-core-node-c" />
          <span className="margince-core-node margince-core-node-d" />
        </div>
        <div className="margince-core-orbit margince-core-orbit-approval">
          <span className="margince-core-gate" />
        </div>
        <span className="margince-core-thread margince-core-thread-a" />
        <span className="margince-core-thread margince-core-thread-b" />
        <span className="margince-core-thread margince-core-thread-c" />
        <div className="margince-core-mark-shell">
          <MarginceMark />
        </div>
      </div>
      {children && <div className="margince-core-content">{children}</div>}
    </div>
  );
}

function MarginceMark() {
  return (
    <svg viewBox="0 0 299 230" role="presentation" focusable="false">
      <path
        className="margince-core-mark-soft"
        d="M141.688 223.911V212.017C141.688 210.362 142.722 209.259 143.239 208.914L160.821 191.849C166.613 186.47 172.198 193.4 172.198 197.02V223.911C172.198 228.048 168.061 229.427 165.993 229.599H147.376C143.239 229.599 141.86 225.807 141.688 223.911Z"
      />
      <path
        className="margince-core-mark-mid"
        d="M191.312 223.907V164.954C191.312 163.299 192.347 162.196 192.864 161.852L210.446 144.786C216.238 139.408 221.823 146.338 221.823 149.957V223.907C221.823 228.044 217.686 229.423 215.618 229.595H197.001C192.864 229.595 191.485 225.803 191.312 223.907Z"
      />
      <path
        className="margince-core-mark-soft"
        d="M241 223.886V112.704C241 111.049 242.034 109.946 242.551 109.602L260.134 92.5361C265.926 87.1579 271.511 94.0875 271.511 97.7074V223.886C271.511 228.023 267.374 229.402 265.305 229.574H246.688C242.551 229.574 241.172 225.782 241 223.886Z"
      />
      <path d="M0 29.4771V213.06C0 232.09 40.8535 237.882 40.8535 212.025V94.636C40.8535 90.9127 44.9906 91.5196 46.0249 92.5675C72.2263 119.114 125.974 173.344 131.352 177.895C136.73 182.445 142.556 179.791 144.797 177.895C187.202 135.49 272.219 50.369 273.046 49.128C273.874 47.887 275.115 48.611 275.632 49.128C278.735 52.403 285.147 59.057 285.975 59.471C293.732 65.159 298.386 59.643 298.386 55.851V9.826C298.386 0 286.492 0 280.803 0H235.296C228.573 0 228.573 8.274 230.124 9.826C233.917 13.963 241.812 22.444 243.053 23.271C244.294 24.098 244.259 24.995 244.087 25.34C210.301 58.264 144.797 116.356 142.729 118.424C140.66 120.493 138.419 119.286 137.557 118.424L31.028 16.032C15.721 0.724 0 20.169 0 29.477Z" />
    </svg>
  );
}
