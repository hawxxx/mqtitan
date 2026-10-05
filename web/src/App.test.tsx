import { it, expect, afterEach, vi } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  Dashboard,
  Wizard,
  Brokers,
  ScenarioEditor,
  BrokerTelemetry,
} from "./App";
import userEvent from "@testing-library/user-event";
import { parse } from "yaml";
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});
it("lets the wizard select a certificate profile for secure WebSocket tests", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(
          JSON.stringify({
            data: [
              {
                id: "saved-tls-profile",
                name: "Production client",
                hasCa: true,
                hasClientCertificate: true,
                expiresAt: "2030-01-01T00:00:00Z",
              },
            ],
          }),
        ),
    ),
  );
  let output = "";
  render(
    <Wizard
      done={(yaml) => {
        output = yaml;
      }}
      close={() => {}}
    />,
  );
  await userEvent.clear(screen.getByLabelText("Broker URL"));
  await userEvent.type(
    screen.getByLabelText("Broker URL"),
    "wss://mqtt.example.com:443/mqtt",
  );
  await screen.findByRole("option", { name: /Production client/ });
  await userEvent.selectOptions(
    screen.getByLabelText("TLS certificate profile"),
    "saved-tls-profile",
  );
  await userEvent.type(
    screen.getByLabelText("Server name / SNI (optional)"),
    "mqtt.example.com",
  );
  await userEvent.click(
    screen.getByRole("button", { name: "Review scenario" }),
  );
  expect(parse(output).broker.tls).toEqual({
    profile: "saved-tls-profile",
    serverName: "mqtt.example.com",
  });
  expect(output).not.toContain("PRIVATE KEY");
});
it("shows an honest empty dashboard when there are no recorded runs", () => {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <Dashboard tests={[]} open={() => {}} create={() => {}} />
    </QueryClientProvider>,
  );
  expect(
    screen.getByText("Your performance baseline starts here"),
  ).toBeTruthy();
  expect(screen.queryByText("12,480")).toBeNull();
});
it("creates a scenario from wizard inputs and presents YAML for review", async () => {
  let output = "";
  render(
    <Wizard
      done={(s) => {
        output = s;
      }}
      close={() => {}}
    />,
  );
  await userEvent.click(
    screen.getByRole("button", { name: "Review scenario" }),
  );
  expect(output).toContain("mqtitan.io/v1alpha1");
  expect(output).toContain("mqtt://localhost:1883");
});
it("persists a broker profile for use in future sessions", async () => {
  localStorage.clear();
  render(
    <QueryClientProvider client={new QueryClient()}>
      <Brokers />
    </QueryClientProvider>,
  );
  await userEvent.type(screen.getByLabelText("Profile name"), "Local lab");
  await userEvent.click(screen.getByRole("button", { name: "Save profile" }));
  expect(
    JSON.parse(localStorage.getItem("mqtitan.brokers") || "[]")[0].name,
  ).toBe("Local lab");
});
it("launches a distributed scenario with selected worker IDs", async () => {
  const original = globalThis.fetch;
  let submitted: any;
  globalThis.fetch = async (input, init) => {
    const path = String(input);
    if (path.endsWith("/workers"))
      return new Response(
        JSON.stringify({
          data: [
            {
              id: "worker-a",
              status: "ready",
              cpuCount: 8,
              memoryBytes: 1000000,
              connected: 0,
              lastSeen: "2026-10-04T00:00:00Z",
            },
          ],
        }),
      );
    if (path.endsWith("/scenarios"))
      return new Response(JSON.stringify({ data: [] }));
    submitted = JSON.parse(String(init?.body));
    return new Response(JSON.stringify({ data: { id: "distributed-run" } }));
  };
  try {
    render(
      <QueryClientProvider
        client={
          new QueryClient({ defaultOptions: { queries: { retry: false } } })
        }
      >
        <ScenarioEditor onStarted={() => {}} />
      </QueryClientProvider>,
    );
    await userEvent.click(await screen.findByLabelText("worker-a"));
    await userEvent.click(screen.getByRole("button", { name: "Start test" }));
    expect(submitted.workers).toEqual(["worker-a"]);
    expect(submitted.scenario).toContain("mqtitan.io/v1alpha1");
  } finally {
    globalThis.fetch = original;
  }
});
it("shows actual EMQX connection and delivery metrics when configured", async () => {
  const original = globalThis.fetch;
  globalThis.fetch = async () =>
    new Response(
      JSON.stringify({
        data: {
          configured: true,
          status: "ok",
          snapshot: {
            timestamp: "2026-10-04T00:00:00Z",
            nodes: [],
            metrics: {},
            connections: 47,
            messagesReceived: 102,
            messagesSent: 99,
            messagesDropped: 3,
          },
        },
      }),
    );
  try {
    render(
      <QueryClientProvider client={new QueryClient()}>
        <BrokerTelemetry />
      </QueryClientProvider>,
    );
    expect(await screen.findByText("47")).toBeTruthy();
    expect(screen.getByText("102")).toBeTruthy();
    expect(screen.getByText("3")).toBeTruthy();
  } finally {
    globalThis.fetch = original;
  }
});
