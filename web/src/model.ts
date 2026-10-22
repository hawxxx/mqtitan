import { stringify } from "yaml";
export interface LabelCount {
  key: string;
  label?: string;
  connects: number;
  active: number;
}
export interface Snapshot {
  connected: number;
  targetClients: number;
  connecting: number;
  connectAttempts: number;
  connectErrors: number;
  publishAttempts: number;
  published: number;
  publishErrors: number;
  received: number;
  bytesSent: number;
  bytesReceived: number;
  p50: number;
  p95: number;
  p99: number;
  p75?: number;
  p90?: number;
  p999?: number;
  min?: number;
  max?: number;
  avg?: number;
  publishLatencyCount?: number;
  histogram?: { upperBoundNs: number; count: number }[];
  correlationSamples?: number;
  endToEndP95?: number;
  errors?: Record<string, number>;
  connectFailures?: Record<string, number>;
  connackReasons?: Record<string, number>;
  remoteAddresses?: LabelCount[];
  addressLabels?: LabelCount[];
  dnsAnswers?: LabelCount[];
  dnsErrors?: LabelCount[];
  peakConnected: number;
  subscriptions?: number;
  subscribeErrors?: number;
  reconnects?: number;
  churned?: number;
  churnReconnects?: number;
  disconnects?: number;
  publishSlotsMissed?: number;
  correlationRejected?: number;
  publishCancelled?: number;
}
export interface Test {
  id: string;
  name: string;
  status: string;
  startedAt: string;
  endedAt?: string;
  scenario: string;
  snapshot: Snapshot;
  thresholds: {
    name: string;
    expression: string;
    observed: string;
    passed: boolean;
  }[];
  error?: string;
  workerIds?: string[] | null;
  lostWorkers?: string[];
  sourceTestId?: string;
  loadControl?: {
    capacityClients: number;
    maxRatePerClient: number;
    hasPublishers: boolean;
    clients: number;
    ratePerClient: number;
    revision: number;
    manual: boolean;
    pendingWorkers?: string[];
  };
  loadChanges?: {
    timestamp: string;
    clients: number;
    ratePerClient: number;
    revision: number;
  }[];
}
export interface Sample {
  timestamp: string;
  snapshot: Snapshot;
  messageRate: number;
  byteRate: number;
}
export interface SavedScenario {
  id: string;
  name: string;
  yaml: string;
  createdAt: string;
}
export interface Worker {
  id: string;
  status: string;
  cpuCount: number;
  memoryBytes: number;
  connected: number;
  lastSeen: string;
}
export interface Check {
  Name: string;
  Value: string;
  Status: string;
  Recommendation: string;
}
export interface WizardValues {
  name: string;
  broker: string;
  clients: number;
  capacity?: number;
  rate: number;
  duration: number;
  qos: number;
  username?: string;
  password?: string;
  version?: string;
  payloadSize?: number;
  tls?: { profile?: string; serverName?: string };
}
export function makeScenario(v: WizardValues): string {
  const capacity = v.capacity ?? v.clients;
  const payloadSize = v.payloadSize ?? 256;
  if (!v.name.trim()) throw new Error("Enter a test name.");
  if (!Number.isInteger(v.clients) || v.clients < 1 || v.clients > 1000000)
    throw new Error("Clients must be a whole number from 1 to 1,000,000.");
  if (!Number.isInteger(capacity) || capacity < v.clients || capacity > 1000000)
    throw new Error(
      "Maximum clients must be a whole number between Clients and 1,000,000.",
    );
  if (!(v.rate > 0) || v.rate > 1000000)
    throw new Error(
      "Messages per client must be above 0 and at most 1,000,000.",
    );
  if (!(v.duration > 0)) throw new Error("Duration must be positive.");
  if (![0, 1, 2].includes(v.qos)) throw new Error("QoS must be 0, 1, or 2.");
  if (
    !Number.isInteger(payloadSize) ||
    payloadSize < 1 ||
    payloadSize > 16 << 20
  )
    throw new Error("Payload size must be from 1 byte to 16 MiB.");
  const broker = v.broker.replace(/^tcp:/, "mqtt:").replace(/^ssl:/, "mqtts:");
  if (!/^(mqtt|mqtts|ws|wss):\/\/.+/.test(broker))
    throw new Error("Broker URL must use mqtt, mqtts, ws, or wss.");
  const tls = {
    ...(v.tls?.profile ? { profile: v.tls.profile } : {}),
    ...(v.tls?.serverName?.trim()
      ? { serverName: v.tls.serverName.trim() }
      : {}),
  };
  if (Object.keys(tls).length && !/^(mqtts|wss):\/\//.test(broker))
    throw new Error("Certificate settings require mqtts:// or wss://.");
  return stringify({
    apiVersion: "mqtitan.io/v1alpha1",
    kind: "Scenario",
    name: v.name,
    broker: {
      url: broker,
      version: v.version || "3.1.1",
      connectTimeout: "10s",
      keepAlive: "30s",
      cleanStart: true,
      ...(v.username ? { username: v.username } : {}),
      ...(v.password ? { password: v.password } : {}),
      ...(Object.keys(tls).length ? { tls } : {}),
    },
    clients: { count: capacity, idTemplate: "mqtitan-${sequence:08}" },
    stages: [{ duration: `${v.duration}s`, targetClients: v.clients }],
    workloads: [
      {
        name: "telemetry",
        type: "publisher",
        clients: capacity,
        topic: "devices/${clientId}/telemetry",
        qos: v.qos,
        ratePerClient: v.rate,
        payload: { type: "random", size: payloadSize },
      },
    ],
  });
}
// Durations arrive in nanoseconds; pick the unit that keeps them readable.
export const latency = (n: number) => {
  const ns = n || 0;
  const size = Math.abs(ns);
  if (size >= 1e9) return `${(ns / 1e9).toFixed(2)} s`;
  if (size >= 999.5e3 || ns === 0) return `${(ns / 1e6).toFixed(2)} ms`;
  return `${Math.round(ns / 1e3)} µs`;
};
export const number = (n: number) =>
  new Intl.NumberFormat("en-US", {
    maximumFractionDigits: 1,
    notation: Math.abs(n) >= 1e6 ? "compact" : "standard",
  }).format(n || 0);
export const bytes = (n: number) => {
  const size = Math.abs(n || 0);
  return size >= 1073741824
    ? `${(n / 1073741824).toFixed(2)} GB`
    : size >= 1048576
      ? `${(n / 1048576).toFixed(1)} MB`
      : size >= 1024
        ? `${(n / 1024).toFixed(1)} KB`
        : `${n || 0} B`;
};
// Elapsed seconds as m:ss or h:mm:ss for chart axes and run durations.
export const elapsed = (seconds: number) => {
  const total = Math.max(0, Math.round(seconds));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = String(total % 60).padStart(2, "0");
  return h ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
};
export function headers() {
  const token = sessionStorage.getItem("mqtitan.token");
  return {
    "Content-Type": "application/json",
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  };
}
export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    ...init,
    headers: { ...headers(), ...init?.headers },
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok)
    throw new Error(
      typeof body.error === "string"
        ? body.error
        : body.error?.message ||
            body.message ||
            `Request failed (${response.status})`,
    );
  return body.data as T;
}
export async function download(id: string, format: string) {
  const res = await fetch(
    `/api/v1/tests/${encodeURIComponent(id)}/export?format=${format}`,
    { headers: headers() },
  );
  if (!res.ok) throw new Error(`Export failed (${res.status})`);
  const url = URL.createObjectURL(await res.blob());
  const a = document.createElement("a");
  a.href = url;
  a.download = `mqtitan-${id}.${format}`;
  a.click();
  // Revoking synchronously can cancel the download before the browser reads the blob.
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

// Run-quality warnings derived from generator-side counters: at least 1% of
// publish slots skipped, at least 0.1% of correlated samples rejected for
// clock skew, or any lost worker.
export function runWarnings(t: Test): string[] {
  const s = t.snapshot;
  const out: string[] = [];
  const missed = s.publishSlotsMissed || 0;
  const scheduled = missed + (s.publishAttempts || 0);
  if (scheduled > 0 && missed / scheduled >= 0.01)
    out.push(
      `Generator saturation: ${((missed / scheduled) * 100).toFixed(1)}% of scheduled publish slots were skipped because earlier publishes had not completed. The achieved rate is below the requested rate; check worker CPU and broker acknowledgement latency.`,
    );
  const rejected = s.correlationRejected || 0;
  const correlated = rejected + (s.correlationSamples || 0);
  // A wall-clock step can reject a handful of samples on a single host; warn
  // when rejections are large enough to bias the percentiles.
  if (correlated > 0 && rejected / correlated >= 0.001)
    out.push(
      `${s.correlationRejected} correlated messages carried timestamps in the future or older than ten minutes and were excluded from end-to-end latency. Synchronize publisher and subscriber clocks.`,
    );
  if (t.lostWorkers?.length)
    out.push(
      `Lost workers: ${t.lostWorkers.join(", ")}. Their partitions stopped early; results include their last reported counters only.`,
    );
  return out;
}
