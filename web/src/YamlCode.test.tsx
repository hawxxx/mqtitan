import { afterEach, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { tokenizeYamlLine, YamlEditor, YamlView } from "./YamlCode";

afterEach(cleanup);

const kinds = (line: string) =>
  tokenizeYamlLine(line)
    .filter((t) => t.kind !== "plain" || t.text.trim())
    .map((t) => `${t.kind}:${t.text.trim()}`);

it("classifies keys, scalars and comments", () => {
  expect(kinds("  rate: 100 # per second")).toEqual([
    "key:rate",
    "punct::",
    "number:100",
    "comment:# per second",
  ]);
  expect(kinds('- url: "mqtt://broker:1883"')).toEqual([
    "punct:-",
    "key:url",
    "punct::",
    'string:"mqtt://broker:1883"',
  ]);
  expect(kinds("  retain: false")).toEqual([
    "key:retain",
    "punct::",
    "literal:false",
  ]);
  expect(kinds("  - topic/a/b")).toEqual(["punct:-", "plain:topic/a/b"]);
  expect(kinds("  idTemplate: demo-${sequence:08}")).toEqual([
    "key:idTemplate",
    "punct::",
    "plain:demo-${sequence:08}",
  ]);
  expect(kinds("broker: mqtt://host:1883")).toEqual([
    "key:broker",
    "punct::",
    "plain:mqtt://host:1883",
  ]);
});

it("never drops characters", () => {
  const lines = [
    "apiVersion: mqtitan.io/v1alpha1",
    "  - { a: 1, b: [x, 'y'] }  # trailing",
    "payload: |",
    "    raw text: with colon",
    "---",
    "\tweird\t: value",
  ];
  for (const line of lines)
    expect(
      tokenizeYamlLine(line)
        .map((t) => t.text)
        .join(""),
    ).toBe(line);
});

it("renders highlighted read-only YAML", () => {
  const { container } = render(<YamlView source={"name: demo\nclients: 5"} />);
  expect(container.querySelector("pre")?.textContent).toBe(
    "name: demo\nclients: 5",
  );
  expect(container.querySelectorAll(".yaml-key")).toHaveLength(2);
  expect(container.querySelector(".yaml-number")?.textContent).toBe("5");
});

it("keeps the editor a plain labelled textarea", () => {
  let value = "";
  render(
    <YamlEditor
      label="Scenario YAML"
      value="a: 1"
      onChange={(v) => (value = v)}
    />,
  );
  fireEvent.change(screen.getByLabelText("Scenario YAML"), {
    target: { value: "a: 2" },
  });
  expect(value).toBe("a: 2");
});
