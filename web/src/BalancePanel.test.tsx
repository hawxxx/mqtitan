import { render, screen, cleanup } from "@testing-library/react";
import { describe, it, expect, afterEach } from "vitest";
import { BalancePanel } from "./BalancePanel";
import type { Balance } from "./model";
afterEach(cleanup);

const stat = (label: string) => screen.getByText(label).nextElementSibling?.textContent;

const balance = (over: Partial<Balance> = {}): Balance => ({
  timestamp: "2026-01-01T00:00:00Z",
  complete: true,
  targets: [
    { key: "emqx-0", connections: 95 },
    { key: "emqx-1", connections: 105 },
  ],
  zones: [
    { key: "zone-a", connections: 95 },
    { key: "zone-b", connections: 105 },
  ],
  targetCV: 0.05,
  targetRatio: 105 / 95,
  zoneSkew: 0.1,
  zoneRatio: 105 / 95,
  ...over,
});

describe("BalancePanel", () => {
  it("shows the coefficient of variation, ratio and skew", () => {
    render(<BalancePanel balance={balance()} />);
    expect(stat("Targets")).toBe("2");
    expect(stat("CV")).toBe("5.0%");
    expect(stat("Max/min")).toBe("1.11");
    expect(stat("Zone skew")).toBe("10.0%");
    expect(screen.getByText("emqx-0")).toBeTruthy();
    expect(screen.getByText("zone-b")).toBeTruthy();
    expect(screen.queryByText(/incomplete telemetry/i)).toBeNull();
  });
  it("shows the incomplete telemetry banner", () => {
    render(<BalancePanel balance={balance({ complete: false })} />);
    expect(screen.getByText(/incomplete telemetry/i)).toBeTruthy();
  });
  it("notes missing zone labels and an infinite ratio", () => {
    render(<BalancePanel balance={balance({ zones: [], targetRatio: null })} />);
    expect(screen.getByText(/zone labels missing/i)).toBeTruthy();
    expect(stat("Max/min")).toBe("∞");
  });
  it("shows no observations for an all-zero fleet", () => {
    render(
      <BalancePanel
        balance={balance({ targets: [{ key: "a", connections: 0 }, { key: "b", connections: 0 }], zones: [{ key: "z", connections: 0 }] })}
      />,
    );
    expect(screen.getByText("No observations")).toBeTruthy();
    expect(screen.queryByText("CV")).toBeNull();
    expect(stat("Zone skew")).toBe("—");
  });
  it("explains why a single target has no spread statistics", () => {
    render(
      <BalancePanel
        balance={balance({ targets: [{ key: "emqx-0", connections: 50000 }], zones: [{ key: "local", connections: 50000 }] })}
      />,
    );
    expect(stat("Targets")).toBe("1");
    expect(screen.queryByText("No observations")).toBeNull();
    expect(screen.getByText(/need at least two targets/)).toBeTruthy();
  });
  it("renders nothing without balance data", () => {
    const { container } = render(<BalancePanel balance={undefined} />);
    expect(container.textContent).toBe("");
  });
});
