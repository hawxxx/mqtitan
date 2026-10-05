import { describe, it, expect } from "vitest";
import { makeScenario, latency, request } from "./model";
import { parse } from "yaml";
describe("scenario wizard", () => {
  it("allows spare client capacity for later live increases", () => {
    const s = parse(
      makeScenario({
        name: "live",
        broker: "mqtt://localhost:1883",
        clients: 100,
        capacity: 1000,
        rate: 1,
        duration: 60,
        qos: 1,
      }),
    );
    expect(s.clients.count).toBe(1000);
    expect(s.stages[0].targetClients).toBe(100);
    expect(s.workloads[0].clients).toBe(1000);
  });
  it("includes a certificate profile and SNI without putting PEM secrets in YAML", () => {
    const s = parse(
      makeScenario({
        name: "TLS",
        broker: "wss://mqtt.example.com:443/mqtt",
        clients: 5,
        rate: 1,
        duration: 10,
        qos: 1,
        tls: { profile: "profile-id", serverName: "mqtt.example.com" },
      }),
    );
    expect(s.broker.tls).toEqual({
      profile: "profile-id",
      serverName: "mqtt.example.com",
    });
  });
  it("defaults to publishers without quadratic subscription fanout", () => {
    const s = parse(
      makeScenario({
        name: "baseline",
        broker: "mqtt://localhost:1883",
        clients: 100000,
        rate: 1,
        duration: 60,
        qos: 1,
      }),
    );
    expect(s.workloads[0].type).toBe("publisher");
    expect(s.workloads[0].topic).toBe("devices/${clientId}/telemetry");
  });
  it("generates executable YAML with the selected connection and load parameters", () => {
    const s = parse(
      makeScenario({
        name: "My load",
        broker: "mqtt://localhost:1883",
        clients: 25,
        rate: 40,
        duration: 60,
        qos: 1,
      }),
    );
    expect(s.name).toBe("My load");
    expect(s.broker.url).toBe("mqtt://localhost:1883");
    expect(s.clients.count).toBe(25);
    expect(s.workloads[0].ratePerClient).toBe(40);
    expect(s.stages[0].duration).toBe("60s");
  });
  it("rejects invalid load values before starting a run", () => {
    expect(() =>
      makeScenario({
        name: "bad",
        broker: "tcp://localhost:1883",
        clients: 0,
        rate: 1,
        duration: 10,
        qos: 0,
      }),
    ).toThrow();
  });
  it("converts nanosecond latency to milliseconds", () =>
    expect(latency(1500000)).toBe("1.50 ms"));
  it("surfaces API error detail instead of accepting an unsuccessful response", async () => {
    const original = globalThis.fetch;
    globalThis.fetch = async () =>
      new Response(JSON.stringify({ error: "Broker refused connection" }), {
        status: 422,
      });
    try {
      await expect(request("/tests")).rejects.toThrow(
        "Broker refused connection",
      );
    } finally {
      globalThis.fetch = original;
    }
  });
});
