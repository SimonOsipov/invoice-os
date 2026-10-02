// Icon primitives. The prototype built its SVGs imperatively via
// React.createElement (icMulti/ic helpers); here they are a single declarative
// component. All icons are stroke-based, 24x24 viewBox, currentColor — the
// parent sets color/size to match the design.

import markUrl from "@invoice-os/design-tokens/v2/assets/mark.png";

// Lucide strokes as path data, 24x24 grid.
export const circlePath = (cx: number, cy: number, r: number) =>
  `M${cx - r} ${cy}a${r} ${r} 0 1 0 ${2 * r} 0a${r} ${r} 0 1 0 ${-2 * r} 0`;
export const rectPath = (x: number, y: number, w: number, h: number, rx = 0) =>
  rx
    ? `M${x + rx} ${y}h${w - 2 * rx}a${rx} ${rx} 0 0 1 ${rx} ${rx}v${h - 2 * rx}a${rx} ${rx} 0 0 1 ${-rx} ${rx}h${-(w - 2 * rx)}a${rx} ${rx} 0 0 1 ${-rx} ${-rx}v${-(h - 2 * rx)}a${rx} ${rx} 0 0 1 ${rx} ${-rx}Z`
    : `M${x} ${y}h${w}v${h}h${-w}Z`;
export const linePath = (x1: number, y1: number, x2: number, y2: number) =>
  `M${x1} ${y1}L${x2} ${y2}`;
export const polylinePath = (...pts: [number, number][]) =>
  "M" + pts.map(([x, y]) => `${x} ${y}`).join("L");

export type GlyphName =
  | "activity"
  | "archive"
  | "building-2"
  | "chart-column"
  | "chart-line"
  | "check"
  | "check-check"
  | "chevron-down"
  | "chevron-left"
  | "chevron-right"
  | "chevron-up"
  | "circle-check"
  | "contact"
  | "file-check"
  | "file-search"
  | "file-text"
  | "globe"
  | "key-round"
  | "layout-dashboard"
  | "link"
  | "loader-circle"
  | "menu"
  | "plug"
  | "send"
  | "settings"
  | "shield-check"
  | "sparkles"
  | "triangle-alert"
  | "user-check"
  | "users"
  | "users-round"
  | "workflow"
  | "x"
  | "play";

// lucide-static@0.428.0, converted by the helpers above.
export const GLYPHS: Record<GlyphName, readonly string[]> = {
  activity: [
    "M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2",
  ],
  archive: [
    "M3 3h18a1 1 0 0 1 1 1v3a1 1 0 0 1 -1 1h-18a1 1 0 0 1 -1 -1v-3a1 1 0 0 1 1 -1Z",
    "M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8",
    "M10 12h4",
  ],
  "building-2": [
    "M6 22V4a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v18Z",
    "M6 12H4a2 2 0 0 0-2 2v6a2 2 0 0 0 2 2h2",
    "M18 9h2a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2h-2",
    "M10 6h4",
    "M10 10h4",
    "M10 14h4",
    "M10 18h4",
  ],
  "chart-column": [
    "M3 3v16a2 2 0 0 0 2 2h16",
    "M18 17V9",
    "M13 17V5",
    "M8 17v-3",
  ],
  "chart-line": ["M3 3v16a2 2 0 0 0 2 2h16", "m19 9-5 5-4-4-3 3"],
  check: ["M20 6 9 17l-5-5"],
  "check-check": ["M18 6 7 17l-5-5", "m22 10-7.5 7.5L13 16"],
  "chevron-down": ["m6 9 6 6 6-6"],
  "chevron-left": ["m15 18-6-6 6-6"],
  "chevron-right": ["m9 18 6-6-6-6"],
  "chevron-up": ["m18 15-6-6-6 6"],
  "circle-check": ["M2 12a10 10 0 1 0 20 0a10 10 0 1 0 -20 0", "m9 12 2 2 4-4"],
  contact: [
    "M17 18a2 2 0 0 0-2-2H9a2 2 0 0 0-2 2",
    "M5 4h14a2 2 0 0 1 2 2v14a2 2 0 0 1 -2 2h-14a2 2 0 0 1 -2 -2v-14a2 2 0 0 1 2 -2Z",
    "M10 10a2 2 0 1 0 4 0a2 2 0 1 0 -4 0",
    "M8 2L8 4",
    "M16 2L16 4",
  ],
  "file-check": [
    "M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z",
    "M14 2v4a2 2 0 0 0 2 2h4",
    "m9 15 2 2 4-4",
  ],
  "file-search": [
    "M14 2v4a2 2 0 0 0 2 2h4",
    "M4.268 21a2 2 0 0 0 1.727 1H18a2 2 0 0 0 2-2V7l-5-5H6a2 2 0 0 0-2 2v3",
    "m9 18-1.5-1.5",
    "M2 14a3 3 0 1 0 6 0a3 3 0 1 0 -6 0",
  ],
  "file-text": [
    "M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z",
    "M14 2v4a2 2 0 0 0 2 2h4",
    "M10 9H8",
    "M16 13H8",
    "M16 17H8",
  ],
  globe: [
    "M2 12a10 10 0 1 0 20 0a10 10 0 1 0 -20 0",
    "M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20",
    "M2 12h20",
  ],
  "key-round": [
    "M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z",
    "M16 7.5a0.5 0.5 0 1 0 1 0a0.5 0.5 0 1 0 -1 0",
  ],
  "layout-dashboard": [
    "M4 3h5a1 1 0 0 1 1 1v7a1 1 0 0 1 -1 1h-5a1 1 0 0 1 -1 -1v-7a1 1 0 0 1 1 -1Z",
    "M15 3h5a1 1 0 0 1 1 1v3a1 1 0 0 1 -1 1h-5a1 1 0 0 1 -1 -1v-3a1 1 0 0 1 1 -1Z",
    "M15 12h5a1 1 0 0 1 1 1v7a1 1 0 0 1 -1 1h-5a1 1 0 0 1 -1 -1v-7a1 1 0 0 1 1 -1Z",
    "M4 16h5a1 1 0 0 1 1 1v3a1 1 0 0 1 -1 1h-5a1 1 0 0 1 -1 -1v-3a1 1 0 0 1 1 -1Z",
  ],
  link: [
    "M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71",
    "M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71",
  ],
  "loader-circle": ["M21 12a9 9 0 1 1-6.219-8.56"],
  menu: ["M4 12L20 12", "M4 6L20 6", "M4 18L20 18"],
  plug: [
    "M12 22v-5",
    "M9 8V2",
    "M15 8V2",
    "M18 8v5a4 4 0 0 1-4 4h-4a4 4 0 0 1-4-4V8Z",
  ],
  send: ["m22 2-7 20-4-9-9-4Z", "M22 2 11 13"],
  settings: [
    "M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z",
    "M9 12a3 3 0 1 0 6 0a3 3 0 1 0 -6 0",
  ],
  "shield-check": [
    "M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z",
    "m9 12 2 2 4-4",
  ],
  sparkles: [
    "M9.937 15.5A2 2 0 0 0 8.5 14.063l-6.135-1.582a.5.5 0 0 1 0-.962L8.5 9.936A2 2 0 0 0 9.937 8.5l1.582-6.135a.5.5 0 0 1 .963 0L14.063 8.5A2 2 0 0 0 15.5 9.937l6.135 1.581a.5.5 0 0 1 0 .964L15.5 14.063a2 2 0 0 0-1.437 1.437l-1.582 6.135a.5.5 0 0 1-.963 0z",
    "M20 3v4",
    "M22 5h-4",
    "M4 17v2",
    "M5 18H3",
  ],
  "triangle-alert": [
    "m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3",
    "M12 9v4",
    "M12 17h.01",
  ],
  "user-check": [
    "M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2",
    "M5 7a4 4 0 1 0 8 0a4 4 0 1 0 -8 0",
    "M16 11L18 13L22 9",
  ],
  users: [
    "M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2",
    "M5 7a4 4 0 1 0 8 0a4 4 0 1 0 -8 0",
    "M22 21v-2a4 4 0 0 0-3-3.87",
    "M16 3.13a4 4 0 0 1 0 7.75",
  ],
  "users-round": [
    "M18 21a8 8 0 0 0-16 0",
    "M5 8a5 5 0 1 0 10 0a5 5 0 1 0 -10 0",
    "M22 20c0-3.37-2-6.5-4-8a5 5 0 0 0-.45-8.3",
  ],
  workflow: [
    "M5 3h4a2 2 0 0 1 2 2v4a2 2 0 0 1 -2 2h-4a2 2 0 0 1 -2 -2v-4a2 2 0 0 1 2 -2Z",
    "M7 11v4a2 2 0 0 0 2 2h4",
    "M15 13h4a2 2 0 0 1 2 2v4a2 2 0 0 1 -2 2h-4a2 2 0 0 1 -2 -2v-4a2 2 0 0 1 2 -2Z",
  ],
  x: ["M18 6 6 18", "m6 6 12 12"],
  play: ["M6 3L20 12L6 21L6 3Z"],
};

type IconProps = {
  paths: readonly string[];
  size?: number;
  strokeWidth?: number;
};

export function Icon({ paths, size = 16, strokeWidth = 1.6 }: IconProps) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {paths.map((d, i) => (
        <path key={i} d={d} />
      ))}
    </svg>
  );
}

// Raster only, never redrawn; the mark's cream corners are rounded by CSS, as in the DS Logo.
// Decorative: the wordmark beside every call site is live text.
export function BrandMark({ size = 22 }: { size?: number }) {
  return (
    <img
      src={markUrl}
      alt=""
      aria-hidden="true"
      width={size}
      height={size}
      style={{ display: "block", borderRadius: "var(--radius-md)" }}
    />
  );
}
