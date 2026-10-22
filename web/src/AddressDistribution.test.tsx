import { render, screen, cleanup, within } from "@testing-library/react";
import { describe, it, expect, afterEach } from "vitest";
import { AddressDistribution } from "./AddressDistribution";
import type { Snapshot } from "./model";
afterEach(cleanup);

describe("AddressDistribution", () => {
  it("shows each label's share of active connections", () => {
    render(
      <AddressDistribution
        snapshot={
          {
            addressLabels: [
              { key: "new-nlb-1a", connects: 1, active: 1 },
              { key: "old-nlb", connects: 1, active: 1 },
            ],
            remoteAddresses: [
              {
                key: "192.0.2.1:1883",
                label: "new-nlb-1a",
                connects: 1,
                active: 1,
              },
              {
                key: "192.0.2.2:1883",
                label: "old-nlb",
                connects: 1,
                active: 1,
              },
            ],
            dnsErrors: [{ key: "192.0.2.53:53", connects: 2, active: 0 }],
          } as Snapshot
        }
      />,
    );
    const row = screen
      .getAllByText("new-nlb-1a", { selector: "td" })[0]
      .closest("tr")!;
    expect(within(row).getByText("50.0%")).toBeTruthy();
    expect(screen.getByText("192.0.2.1:1883")).toBeTruthy();
    expect(screen.getByText("DNS errors")).toBeTruthy();
  });
  it("renders nothing without address data", () => {
    const { container } = render(
      <AddressDistribution snapshot={{} as Snapshot} />,
    );
    expect(container.firstChild).toBeNull();
  });
});
