import { expect, it } from "vitest";
import { readScenarioFiles, scenarioName } from "./scenarioImport";

it("names scenarios from YAML first, then the file", () => {
  expect(scenarioName("name: 50k telemetry\nkind: Scenario", "a.yaml")).toBe(
    "50k telemetry",
  );
  expect(scenarioName("kind: Scenario", "storm.yml")).toBe("storm");
  expect(scenarioName(": : bad", "broken.yaml")).toBe("broken");
});

it("reads YAML files and reports the rest", async () => {
  const result = await readScenarioFiles([
    new File(["name: a\n"], "a.yaml"),
    new File(["x"], "notes.txt"),
    new File([" \n"], "empty.yml"),
    new File(["x".repeat((1 << 20) + 1)], "huge.yaml"),
  ]);
  expect(result.scenarios).toEqual([
    { file: "a.yaml", name: "a", yaml: "name: a\n" },
  ]);
  expect(result.errors).toEqual([
    "notes.txt: not a .yaml or .yml file",
    "empty.yml: empty file",
    "huge.yaml: larger than 1 MiB",
  ]);
});
