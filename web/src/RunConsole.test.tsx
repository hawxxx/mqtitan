import { afterEach, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { deriveConsoleLines, RunConsole } from "./RunConsole";
import type { LogEvent, Sample, Snapshot } from "./model";

afterEach(() => {
  cleanup();
  localStorage.clear();
});

const start = "2026-01-01T00:00:00Z";
const at = (second: number, s: Partial<Snapshot>, messageRate = 0): Sample => ({
  timestamp: new Date(Date.parse(start) + second * 1000).toISOString(),
  messageRate,
  byteRate: 0,
  snapshot: {
    connected: 0,
    targetClients: 100,
    connecting: 0,
    connectAttempts: 0,
    connectErrors: 0,
    publishAttempts: 0,
    published: 0,
    publishErrors: 0,
    received: 0,
    bytesSent: 0,
    bytesReceived: 0,
    p50: 0,
    p95: 0,
    p99: 0,
    peakConnected: 0,
    ...s,
  },
});

it("announces milestones, the target and drops", () => {
  const lines = deriveConsoleLines(
    [
      at(0, {}),
      at(1, { connected: 30 }),
      at(2, { connected: 100 }),
      at(3, { connected: 60 }),
    ],
    start,
  ).map((l) => `${l.level} ${l.message}`);
  expect(lines).toContain("info Connected 30 / 100 (25%)");
  expect(lines).toContain(
    "success All 100 target clients connected after 0:02",
  );
  expect(lines).toContain("warn Connected dropped 100 → 60");
});

it("logs the shutdown drain of a finished run as info", () => {
  const samples = [
    at(0, { connected: 100 }),
    at(1, { connected: 0, disconnects: 100 }),
  ];
  const finished = deriveConsoleLines(samples, start, true).map(
    (l) => `${l.level} ${l.message}`,
  );
  expect(finished).toContain("info +100 clients closed at test end");
  expect(finished.some((l) => l.startsWith("warn"))).toBe(false);
  const live = deriveConsoleLines(samples, start).map((l) => l.level);
  expect(live).toContain("warn");
});

it("batches errors with their reasons into one line per window", () => {
  const samples = [at(0, {})];
  for (let i = 1; i <= 7; i++)
    samples.push(
      at(i, {
        connectErrors: i * 10,
        connectFailures: { timeout: i * 8, refused: i * 2 },
      }),
    );
  const errors = deriveConsoleLines(samples, start).filter(
    (l) => l.level === "error",
  );
  expect(errors).toHaveLength(2);
  expect(errors[0].message).toBe(
    "+60 connection errors: timeout ×48, refused ×12",
  );
});

it("merges server events, colours levels and filters them", () => {
  const events: LogEvent[] = [
    { seq: 1, time: start, level: "info", message: "Test started" },
    {
      seq: 2,
      time: "2026-01-01T00:00:05Z",
      level: "action",
      message: "Live load changed",
    },
    {
      seq: 3,
      time: "2026-01-01T00:00:09Z",
      level: "error",
      message: "Worker lease expired on worker-b",
    },
  ];
  const { container } = render(
    <RunConsole startedAt={start} samples={[]} events={events} running />,
  );
  const log = screen.getByRole("log");
  expect(log.querySelectorAll("li:not(.console-day)")).toHaveLength(3);
  const action = log.querySelector(".level-action");
  expect(action?.querySelector(".console-elapsed")?.textContent).toBe("+0:05");
  expect(action?.textContent).toContain("USERLive load changed");
  expect(action?.querySelector("time")?.textContent).toMatch(
    /^\d\d:\d\d:\d\d\.\d{3}$/,
  );
  fireEvent.click(screen.getByRole("button", { name: "FAIL 1" }));
  expect(log.querySelector(".level-error")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: /Run log/ }));
  expect(screen.queryByRole("log")).toBeNull();
  expect(container.querySelector(".run-console.open")).toBeNull();
});
