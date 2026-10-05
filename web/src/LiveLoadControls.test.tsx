import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { LiveLoadControls } from "./LiveLoadControls";
import type { Test } from "./model";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
const test = {
  id: "live-test",
  name: "Live",
  status: "running",
  snapshot: { targetClients: 100, connected: 100 },
  loadControl: {
    capacityClients: 1000,
    maxRatePerClient: 1000000,
    hasPublishers: true,
    clients: 100,
    ratePerClient: 1,
    revision: 0,
    manual: false,
  },
} as Test;

it("edits drafts without sending traffic changes until Apply is clicked", async () => {
  let body: unknown;
  const updated = vi.fn();
  const fetcher = vi.fn(async (_url, options) => {
    body = JSON.parse(options.body);
    return new Response(
      JSON.stringify({
        data: {
          ...test,
          loadControl: {
            ...test.loadControl,
            clients: 250,
            ratePerClient: 5,
            revision: 1,
            manual: true,
          },
        },
      }),
    );
  });
  vi.stubGlobal("fetch", fetcher);
  render(<LiveLoadControls test={test} onUpdated={updated} />);
  const clients = screen.getByRole("spinbutton", { name: "Client target" });
  await userEvent.clear(clients);
  await userEvent.type(clients, "250");
  const rate = screen.getByRole("spinbutton", {
    name: "Messages / publisher / sec",
  });
  await userEvent.clear(rate);
  await userEvent.type(rate, "5");
  expect(fetcher).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Apply load" }));
  await waitFor(() => expect(updated).toHaveBeenCalled());
  expect(body).toEqual({ clients: 250, ratePerClient: 5, revision: 0 });
});

it("bounds sliders and blocks requests above the scenario capacity", async () => {
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  render(<LiveLoadControls test={test} onUpdated={() => {}} />);
  expect(
    screen
      .getByRole("slider", { name: "Client target slider" })
      .getAttribute("max"),
  ).toBe("1000");
  const clients = screen.getByRole("spinbutton", { name: "Client target" });
  await userEvent.clear(clients);
  await userEvent.type(clients, "1001");
  expect(
    (screen.getByRole("button", { name: "Apply load" }) as HTMLButtonElement)
      .disabled,
  ).toBe(true);
  expect(fetcher).not.toHaveBeenCalled();
});

it("keeps edited drafts during telemetry updates and shows conflicts", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            error: { message: "Live load changed; refresh and retry" },
          }),
          { status: 409 },
        ),
    ),
  );
  const props = { test, onUpdated: () => {} };
  const view = render(<LiveLoadControls {...props} />);
  const clients = screen.getByRole("spinbutton", { name: "Client target" });
  await userEvent.clear(clients);
  await userEvent.type(clients, "300");
  view.rerender(
    <LiveLoadControls
      {...props}
      test={{ ...test, snapshot: { ...test.snapshot, targetClients: 150 } }}
    />,
  );
  expect((clients as HTMLInputElement).value).toBe("300");
  await userEvent.click(screen.getByRole("button", { name: "Apply load" }));
  expect((await screen.findByRole("alert")).textContent).toContain(
    "refresh and retry",
  );
});

it("disables mutation after the run finishes", () => {
  render(
    <LiveLoadControls
      test={{ ...test, status: "completed" }}
      onUpdated={() => {}}
    />,
  );
  expect(
    (screen.getByRole("button", { name: "Apply load" }) as HTMLButtonElement)
      .disabled,
  ).toBe(true);
});

it("uses presets as safe drafts and can discard them without sending requests", async () => {
  const fetcher = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  render(<LiveLoadControls test={test} onUpdated={() => {}} />);
  await userEvent.click(
    screen.getByRole("button", { name: "Set clients to 50% capacity" }),
  );
  await userEvent.click(
    screen.getByRole("button", { name: "Pause publishing" }),
  );
  expect(
    (
      screen.getByRole("spinbutton", {
        name: "Client target",
      }) as HTMLInputElement
    ).value,
  ).toBe("500");
  expect(
    (
      screen.getByRole("spinbutton", {
        name: "Messages / publisher / sec",
      }) as HTMLInputElement
    ).value,
  ).toBe("0");
  expect(screen.getByText("Unapplied changes")).toBeTruthy();
  expect(fetcher).not.toHaveBeenCalled();
  await userEvent.click(screen.getByRole("button", { name: "Reset draft" }));
  expect(
    (
      screen.getByRole("spinbutton", {
        name: "Client target",
      }) as HTMLInputElement
    ).value,
  ).toBe("100");
  expect(
    (
      screen.getByRole("spinbutton", {
        name: "Messages / publisher / sec",
      }) as HTMLInputElement
    ).value,
  ).toBe("1");
  expect(
    (screen.getByRole("button", { name: "Apply load" }) as HTMLButtonElement)
      .disabled,
  ).toBe(true);
  expect(fetcher).not.toHaveBeenCalled();
});

it("disables publisher presets and sliders for connection-only runs", () => {
  render(
    <LiveLoadControls
      test={{
        ...test,
        loadControl: {
          ...test.loadControl!,
          hasPublishers: false,
          ratePerClient: 0,
        },
      }}
      onUpdated={() => {}}
    />,
  );
  expect(
    (
      screen.getByRole("button", {
        name: "Pause publishing",
      }) as HTMLButtonElement
    ).disabled,
  ).toBe(true);
  expect(
    (
      screen.getByRole("slider", {
        name: "Message rate slider",
      }) as HTMLInputElement
    ).disabled,
  ).toBe(true);
});
