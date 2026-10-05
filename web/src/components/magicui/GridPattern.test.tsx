import { afterEach, expect, it } from "vitest";
import { cleanup, render } from "@testing-library/react";
import { GridPattern } from "./GridPattern";

afterEach(cleanup);
it("uses unique pattern IDs and stays out of the accessibility tree", () => {
  const { container } = render(
    <>
      <GridPattern />
      <GridPattern squares={[[1, 2]]} />
    </>,
  );
  const patterns = container.querySelectorAll("pattern");
  expect(patterns[0].id).not.toBe(patterns[1].id);
  expect(
    container.querySelectorAll('svg[aria-hidden="true"][focusable="false"]'),
  ).toHaveLength(2);
  expect(container.querySelectorAll("rect")).toHaveLength(3);
});
