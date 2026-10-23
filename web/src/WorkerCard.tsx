import { Cpu, MemoryStick, Server } from "lucide-react";
import type { Worker } from "./model";
import { bytes, elapsed, number } from "./model";
import { dateTime, useTimeZone } from "./timezone";

const osName: Record<string, string> = {
  linux: "Linux",
  darwin: "macOS",
  windows: "Windows",
};

function Row({ label, value }: { label: string; value?: string }) {
  if (!value) return null;
  return (
    <div className="plan-row">
      <span>{label}</span>
      <strong className="mono">{value}</strong>
    </div>
  );
}

export function WorkerCard({
  worker: w,
  badge,
  now = Date.now(),
}: {
  worker: Worker;
  badge: React.ReactNode;
  now?: number;
}) {
  const zone = useTimeZone();
  const h = w.host;
  const platform = h
    ? [osName[h.os] || h.os, h.arch, h.container].filter(Boolean)
    : [];
  const cores = h?.cpuLimit
    ? `${number(h.cpuLimit)} of ${h.cpus} (limit)`
    : String(h?.cpus || w.cpuCount);
  const memory = h?.memoryTotalBytes
    ? h.memoryLimitBytes && h.memoryLimitBytes < h.memoryTotalBytes
      ? `${bytes(h.memoryLimitBytes)} of ${bytes(h.memoryTotalBytes)} (limit)`
      : bytes(h.memoryTotalBytes)
    : undefined;
  return (
    <section className="panel side-panel worker-card">
      <div className="worker-heading">
        <Server size={22} />
        <h2 className="mono">{w.id}</h2>
        {badge}
      </div>
      {platform.length > 0 && (
        <div className="worker-tags">
          {platform.map((p) => (
            <span key={p}>{p}</span>
          ))}
        </div>
      )}
      {h?.distro && <p className="worker-distro">{h.distro}</p>}
      {h?.cpuModel && (
        <p className="worker-cpu">
          <Cpu size={14} /> {h.cpuModel}
        </p>
      )}
      <div className="worker-stats">
        <div>
          <Cpu size={14} />
          <span>CPU cores</span>
          <strong>{cores}</strong>
        </div>
        <div>
          <MemoryStick size={14} />
          <span>Memory</span>
          <strong>{memory || "—"}</strong>
        </div>
      </div>
      <Row label="Connected clients" value={number(w.connected)} />
      <Row label="Process memory" value={bytes(w.memoryBytes)} />
      <Row
        label="Open files limit"
        value={h?.openFiles ? number(h.openFiles) : undefined}
      />
      <Row
        label="Ephemeral ports"
        value={h?.ephemeralPorts ? number(h.ephemeralPorts) : undefined}
      />
      <Row
        label="Clock offset"
        value={
          Number.isFinite(w.clockOffsetMs)
            ? `${w.clockOffsetMs >= 0 ? "+" : ""}${w.clockOffsetMs.toFixed(1)} ms`
            : undefined
        }
      />
      <Row label="Kernel" value={h?.kernel} />
      <Row label="Hostname" value={h?.hostname} />
      <Row
        label="Build"
        value={
          h
            ? [h.version && `mqtitan ${h.version}`, h.goVersion]
                .filter(Boolean)
                .join(" · ")
            : undefined
        }
      />
      <Row
        label="Uptime"
        value={
          h?.startedAt
            ? elapsed(Math.max(0, now - Date.parse(h.startedAt)) / 1000)
            : undefined
        }
      />
      <small className="subdued">
        Last seen {dateTime(Date.parse(w.lastSeen), zone)}
      </small>
      {!h && (
        <small className="subdued">
          Upgrade this worker to report its OS, CPU and limits.
        </small>
      )}
    </section>
  );
}
