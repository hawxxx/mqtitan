import { parse } from "yaml";

export const MAX_SCENARIO_BYTES = 1 << 20;
export type ImportedScenario = { file: string; name: string; yaml: string };
export type ImportResult = { scenarios: ImportedScenario[]; errors: string[] };

// scenarioName prefers the scenario's own name, then the file name.
export function scenarioName(yaml: string, file: string) {
  try {
    const doc = parse(yaml) as { name?: unknown } | null;
    if (typeof doc?.name === "string" && doc.name.trim())
      return doc.name.trim();
  } catch {
    /* Server-side validation reports YAML errors. */
  }
  return file.replace(/\.ya?ml$/i, "") || "Imported scenario";
}

export async function readScenarioFiles(files: File[]): Promise<ImportResult> {
  const result: ImportResult = { scenarios: [], errors: [] };
  for (const f of files) {
    if (!/\.ya?ml$/i.test(f.name)) {
      result.errors.push(`${f.name}: not a .yaml or .yml file`);
    } else if (f.size > MAX_SCENARIO_BYTES) {
      result.errors.push(`${f.name}: larger than 1 MiB`);
    } else {
      const yaml = await f.text();
      if (!yaml.trim()) result.errors.push(`${f.name}: empty file`);
      else
        result.scenarios.push({
          file: f.name,
          name: scenarioName(yaml, f.name),
          yaml,
        });
    }
  }
  return result;
}
