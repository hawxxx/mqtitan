import { describe, it, expect } from "vitest";
import { bytes, elapsed, makeScenario, latency, request, runWarnings, type Test } from "./model";
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
describe("formatters", () => {
  it("chooses readable latency units", () => {
    expect(latency(80_000)).toBe("80 µs");
    expect(latency(0)).toBe("0.00 ms");
    expect(latency(2_500_000_000)).toBe("2.50 s");
    expect(latency(-2_000_000)).toBe("-2.00 ms");
  });
  it("formats gigabytes and elapsed time", () => {
    expect(bytes(3 * 1073741824)).toBe("3.00 GB");
    expect(elapsed(65)).toBe("1:05");
    expect(elapsed(3725)).toBe("1:02:05");
  });
  it("uses the requested payload size and explains invalid capacity", () => {
    const base = { name: "x", broker: "mqtt://b:1883", clients: 10, rate: 1, duration: 5, qos: 0 };
    expect(makeScenario({ ...base, payloadSize: 1024 })).toContain("size: 1024");
    expect(() => makeScenario({ ...base, capacity: 5 })).toThrow(/Maximum clients/);
  });
});
describe("run warnings", () => {
  const base = {
    id: "t",
    name: "t",
    status: "completed",
    startedAt: "",
    scenario: "",
    thresholds: [],
  };
  it("flags generator saturation, clock skew and lost workers", () => {
    const w = runWarnings({
      ...base,
      lostWorkers: ["w2"],
      snapshot: { publishAttempts: 90, publishSlotsMissed: 10, correlationRejected: 3, correlationSamples: 100 },
    } as unknown as Test);
    expect(w).toHaveLength(3);
    expect(w[0]).toContain("10.0%");
    expect(w[2]).toContain("w2");
  });
  it("flags incomplete broker telemetry", () => {
    const balance = { timestamp: "", complete: false, targets: [], zones: [], targetCV: 0, targetRatio: 1, zoneSkew: 0, zoneRatio: 1 };
    const w = runWarnings({ ...base, snapshot: {}, balance } as unknown as Test);
    expect(w).toEqual(["Broker telemetry was incomplete at the peak sample"]);
    expect(runWarnings({ ...base, snapshot: {}, balance: { ...balance, complete: true } } as unknown as Test)).toEqual([]);
  });
  it("stays quiet for a healthy run", () => {
    expect(
      runWarnings({ ...base, snapshot: { publishAttempts: 1000, publishSlotsMissed: 1, correlationRejected: 26, correlationSamples: 780000 } } as unknown as Test),
    ).toEqual([]);
  });
});
