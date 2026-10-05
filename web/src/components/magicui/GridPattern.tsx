// Adapted from Magic UI's grid-pattern registry component, MIT licensed.
// Source: https://magicui.design/r/grid-pattern.json
// Styling uses this project's CSS instead of Tailwind. See THIRD_PARTY_NOTICES.md.
import { useId, type SVGProps } from "react";

type Props = SVGProps<SVGSVGElement> & {
  width?: number;
  height?: number;
  squares?: Array<[number, number]>;
};

export function GridPattern({
  width = 36,
  height = 36,
  squares = [],
  className = "",
  ...props
}: Props) {
  const id = useId();
  return (
    <svg
      aria-hidden="true"
      focusable="false"
      className={`magic-grid-pattern ${className}`}
      {...props}
    >
      <defs>
        <pattern
          id={id}
          width={width}
          height={height}
          patternUnits="userSpaceOnUse"
          x={-1}
          y={-1}
        >
          <path d={`M.5 ${height}V.5H${width}`} fill="none" />
        </pattern>
      </defs>
      <rect width="100%" height="100%" strokeWidth={0} fill={`url(#${id})`} />
      {squares.map(([x, y]) => (
        <rect
          key={`${x}-${y}`}
          width={width - 1}
          height={height - 1}
          x={x * width}
          y={y * height}
          strokeWidth={0}
        />
      ))}
    </svg>
  );
}
