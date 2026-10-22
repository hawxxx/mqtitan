// Adapted from Magic UI's number-ticker registry component, MIT licensed.
// Source: https://magicui.design/r/number-ticker.json
// The motion/react spring is replaced by a requestAnimationFrame ease, and the
// first render shows the real value instead of counting up from zero, so the
// figure is correct before any animation runs. See THIRD_PARTY_NOTICES.md.
import { useEffect, useRef } from "react";

const reducedMotion = () =>
  typeof window.matchMedia !== "function" ||
  window.matchMedia("(prefers-reduced-motion: reduce)").matches;

export function NumberTicker({
  value,
  format,
  duration = 700,
}: {
  value: number;
  format: (n: number) => string;
  duration?: number;
}) {
  const ref = useRef<HTMLSpanElement>(null);
  const shown = useRef(value);
  const formatRef = useRef(format);
  formatRef.current = format;
  useEffect(() => {
    const el = ref.current;
    const from = shown.current;
    if (!el || from === value || !Number.isFinite(value)) return;
    if (reducedMotion() || !Number.isFinite(from)) {
      shown.current = value;
      el.textContent = formatRef.current(value);
      return;
    }
    const start = performance.now();
    // Read the clock here: rAF timestamps use a different origin in some
    // environments, which would stall the animation.
    let frame = requestAnimationFrame(function step() {
      const t = Math.min(1, (performance.now() - start) / duration);
      const eased = 1 - Math.pow(1 - t, 3);
      shown.current = from + (value - from) * eased;
      el.textContent = formatRef.current(t === 1 ? value : shown.current);
      if (t < 1) frame = requestAnimationFrame(step);
    });
    return () => cancelAnimationFrame(frame);
  }, [value, duration]);
  return <span ref={ref}>{format(shown.current)}</span>;
}
