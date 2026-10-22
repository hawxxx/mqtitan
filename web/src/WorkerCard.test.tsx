import { afterEach, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { WorkerCard } from "./WorkerCard";
import type { Worker } from "./model";

afterEach(cleanup);

const worker: Worker = {
  id: "worker-a",
  status: "ready",
  cpuCount: 32,
  memoryBytes: 20 << 20,
  connected: 25000,
  lastSeen: "2026-01-01T00:00:10Z",
  clockOffsetMs: 0.4,
  host: {
    os: "linux",
    arch: "amd64",
    container: "kubernetes",
    distro: "Alpine Linux v3.20",
    kernel: "6.6.0",
    cpuModel: "AMD EPYC 7R13",
    cpus: 32,
    cpuLimit: 4,
    memoryTotalBytes: 64 * 2 ** 30,
    memoryLimitBytes: 8 * 2 ** 30,
    openFiles: 65536,
    ephemeralPorts: 63977,
    goVersion: "go1.26.0",
    version: "0.9.0",
    startedAt: "2026-01-01T00:00:00Z",
  },
};

it("shows platform, CPU, limits and build details", () => {
  render(
    <WorkerCard
      worker={worker}
      badge={null}
      now={Date.parse("2026-01-01T01:02:03Z")}
    />,
  );
  for (const text of ["Linux", "amd64", "kubernetes", "Alpine Linux v3.20"])
    expect(screen.getByText(text)).toBeTruthy();
  expect(screen.getByText(/AMD EPYC 7R13/)).toBeTruthy();
  expect(screen.getByText("4 of 32 (limit)")).toBeTruthy();
  expect(screen.getByText("8.00 GB of 64.00 GB (limit)")).toBeTruthy();
  expect(screen.getByText("63,977")).toBeTruthy();
  expect(screen.getByText("mqtitan 0.9.0 · go1.26.0")).toBeTruthy();
  expect(screen.getByText("1:02:03")).toBeTruthy();
  expect(screen.getByText("+0.4 ms")).toBeTruthy();
});

it("asks older workers to upgrade for host details", () => {
  render(<WorkerCard worker={{ ...worker, host: undefined }} badge={null} />);
  expect(screen.getByText("32")).toBeTruthy();
  expect(screen.getByText(/Upgrade this worker/)).toBeTruthy();
});
