// Adapted from Magic UI's border-beam registry component, MIT licensed.
// Source: https://magicui.design/r/border-beam.json
// The motion/react animation is replaced by a CSS offset-path keyframe so the
// project needs no animation runtime. See THIRD_PARTY_NOTICES.md.
import type { CSSProperties } from "react";

type Props = {
  size?: number;
  duration?: number;
  delay?: number;
  colorFrom?: string;
  colorTo?: string;
};

export function BorderBeam({
  size = 120,
  duration = 9,
  delay = 0,
  colorFrom = "var(--teal)",
  colorTo = "var(--silver)",
}: Props) {
  return (
    <div aria-hidden="true" className="magic-border-beam">
      <div
        style={
          {
            "--beam-size": `${size}px`,
            "--beam-duration": `${duration}s`,
            "--beam-delay": `${-delay}s`,
            "--beam-from": colorFrom,
            "--beam-to": colorTo,
          } as CSSProperties
        }
      />
    </div>
  );
}
