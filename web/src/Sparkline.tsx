import { useId } from "react";

const POINTS = 48;

// downsample keeps the newest values and averages them into at most POINTS
// buckets, so the path has a fixed size and its `d` can transition smoothly.
export function downsample(values: number[], points = POINTS): number[] {
  const v = values.filter(Number.isFinite);
  if (v.length <= points) return v;
  const out: number[] = [];
  for (let i = 0; i < points; i++) {
    const from = Math.floor((i * v.length) / points);
    const to = Math.max(from + 1, Math.floor(((i + 1) * v.length) / points));
    const slice = v.slice(from, to);
    out.push(slice.reduce((a, b) => a + b, 0) / slice.length);
  }
  return out;
}

export function sparkPaths(values: number[], width = 100, height = 32) {
  const v = downsample(values);
  if (v.length < 2) return undefined;
  const lo = Math.min(...v);
  const hi = Math.max(...v);
  const span = hi - lo || 1;
  const pad = 2;
  const pts = v.map((y, i) => [
    (i / (v.length - 1)) * width,
    height - pad - ((y - lo) / span) * (height - pad * 2),
  ]);
  const line = pts
    .map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(2)} ${y.toFixed(2)}`)
    .join(" ");
  return { line, area: `${line} L${width} ${height} L0 ${height} Z` };
}

export function Sparkline({ values }: { values: number[] }) {
  const id = useId();
  const paths = sparkPaths(values);
  if (!paths) return null;
  return (
    <svg
      className="sparkline"
      viewBox="0 0 100 32"
      preserveAspectRatio="none"
      aria-hidden="true"
      focusable="false"
    >
      <defs>
        <linearGradient id={id} x1="0" x2="0" y1="0" y2="1">
          <stop offset="0%" stopColor="currentColor" stopOpacity="0.32" />
          <stop offset="100%" stopColor="currentColor" stopOpacity="0" />
        </linearGradient>
      </defs>
      <path className="sparkline-area" d={paths.area} fill={`url(#${id})`} />
      <path
        className="sparkline-line"
        d={paths.line}
        fill="none"
        stroke="currentColor"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}
