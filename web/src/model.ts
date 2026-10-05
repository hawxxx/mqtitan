import { stringify } from "yaml";
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
  peakConnected: number;
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
  tls?: { profile?: string; serverName?: string };
}
export function makeScenario(v: WizardValues): string {
  const capacity = v.capacity ?? v.clients;
  if (
    !v.name.trim() ||
    v.clients < 1 ||
    !Number.isInteger(v.clients) ||
    !Number.isInteger(capacity) ||
    capacity < v.clients ||
    capacity > 1000000 ||
    v.rate <= 0 ||
    v.duration <= 0 ||
    ![0, 1, 2].includes(v.qos)
  )
    throw new Error("Enter a name and positive clients, rate, and duration.");
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
        payload: { type: "random", size: 256 },
      },
    ],
  });
}
export const latency = (n: number) => `${((n || 0) / 1e6).toFixed(2)} ms`;
export const number = (n: number) =>
  new Intl.NumberFormat("en-US", {
    maximumFractionDigits: 1,
    notation: Math.abs(n) >= 1e6 ? "compact" : "standard",
  }).format(n || 0);
export const bytes = (n: number) =>
  n >= 1048576
    ? `${(n / 1048576).toFixed(1)} MB`
    : n >= 1024
      ? `${(n / 1024).toFixed(1)} KB`
      : `${n || 0} B`;
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
  URL.revokeObjectURL(url);
}
