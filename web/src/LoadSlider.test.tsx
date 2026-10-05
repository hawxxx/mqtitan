import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { LoadSlider, sliderPosition, sliderValue } from "./LoadSlider";

afterEach(cleanup);

it("maps linear positions to exact bounded client targets", () => {
  expect(sliderValue(0, 1000)).toBe(0);
  expect(sliderValue(0.25, 1000)).toBe(250);
  expect(sliderValue(1, 1000)).toBe(1000);
  expect(sliderValue(2, 1000)).toBe(1000);
  expect(sliderPosition(-1, 1000)).toBe(0);
  expect(sliderPosition(2000, 1000)).toBe(1);
  expect(sliderPosition(NaN, 1000)).toBe(0);
  expect(sliderPosition(10, 0)).toBe(0);
});

it("keeps logarithmic endpoints exact and small rates nonzero", () => {
  const max = 1_000_000;
  expect(sliderValue(0, max, true)).toBe(0);
  expect(sliderValue(1, max, true)).toBe(max);
  for (const rate of [0.001, 0.1, 1, 10, 1000, 100_000]) {
    expect(sliderValue(sliderPosition(rate, max, true), max, true)).toBeCloseTo(
      rate,
      3,
    );
  }
  expect(sliderValue(0.001, max, true)).toBeGreaterThan(0);
});

it("renders a filled track with actual-value accessible semantics", () => {
  const change = vi.fn();
  const { container } = render(
    <LoadSlider
      label="Rate"
      value={10}
      max={1_000_000}
      unit="msg/s"
      logarithmic
      onChange={change}
    />,
  );
  const slider = screen.getByRole("slider", { name: "Rate" });
  expect(slider.getAttribute("aria-valuenow")).toBe("10");
  expect(slider.getAttribute("aria-valuemax")).toBe("1000000");
  expect(slider.getAttribute("aria-valuetext")).toContain("10 msg/s");
  expect(
    (container.firstChild as HTMLElement).style.getPropertyValue(
      "--slider-fill",
    ),
  ).not.toBe("0%");
  fireEvent.change(slider, { target: { value: "1000" } });
  expect(change).toHaveBeenCalledWith(1_000_000);
  fireEvent.change(slider, { target: { value: "0" } });
  expect(change).toHaveBeenCalledWith(0);
});

it("keeps disabled controls and deduplicates ticks for small populations", () => {
  render(
    <LoadSlider
      label="Clients"
      value={1}
      max={1}
      unit="clients"
      disabled
      onChange={() => {}}
    />,
  );
  expect(
    (screen.getByRole("slider", { name: "Clients" }) as HTMLInputElement)
      .disabled,
  ).toBe(true);
  expect(document.querySelectorAll(".load-slider-ticks > span")).toHaveLength(
    2,
  );
});
