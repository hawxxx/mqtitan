import { afterEach, expect, it } from "vitest";
import { cleanup, render, waitFor } from "@testing-library/react";
import { downsample, Sparkline, sparkPaths } from "./Sparkline";
import { NumberTicker } from "./components/magicui/NumberTicker";

afterEach(cleanup);

it("downsamples to a fixed number of averaged points", () => {
  const v = Array.from({ length: 480 }, (_, i) => i);
  const d = downsample(v);
  expect(d).toHaveLength(48);
  expect(d[0]).toBe(4.5);
  expect(downsample([1, 2, 3])).toEqual([1, 2, 3]);
  expect(downsample([1, NaN, 3])).toEqual([1, 3]);
});

it("scales the path to the box and skips flat-free edge cases", () => {
  expect(sparkPaths([5])).toBeUndefined();
  const p = sparkPaths([0, 10])!;
  expect(p.line).toBe("M0.00 30.00 L100.00 2.00");
  expect(p.area.endsWith("L100 32 L0 32 Z")).toBe(true);
  expect(sparkPaths([3, 3, 3])!.line).toContain("30.00");
});

it("anchors counts and rates to zero when asked", () => {
  const free = sparkPaths([99, 100], 100, 32, false)!;
  const anchored = sparkPaths([99, 100], 100, 32, true)!;
  expect(free.line).toBe("M0.00 30.00 L100.00 2.00");
  expect(anchored.line).toBe("M0.00 2.28 L100.00 2.00");
});

it("renders decoratively and only with enough points", () => {
  const { container, rerender } = render(<Sparkline values={[1]} />);
  expect(container.querySelector("svg")).toBeNull();
  rerender(<Sparkline values={[1, 2, 3]} />);
  expect(container.querySelector('svg[aria-hidden="true"]')).not.toBeNull();
});

it("shows the real value immediately and eases to updates", async () => {
  const { container, rerender } = render(
    <NumberTicker
      value={1500}
      format={(n) => Math.round(n).toLocaleString("en-US")}
    />,
  );
  expect(container.textContent).toBe("1,500");
  rerender(
    <NumberTicker
      value={2500}
      format={(n) => Math.round(n).toLocaleString("en-US")}
    />,
  );
  await waitFor(() => expect(container.textContent).toBe("2,500"));
});
