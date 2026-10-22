import { useState } from "react";
import type { Balance, BalanceGroup, FleetSample } from "./model";
import { number } from "./model";

const PAGE_SIZE = 20;
type Order = "most" | "least" | "name";

function Bars({ title, groups }: { title: string; groups: BalanceGroup[] }) {
  const [query, setQuery] = useState("");
  const [order, setOrder] = useState<Order>("most");
  const [page, setPage] = useState(0);
  const max = Math.max(1, ...groups.map((g) => g.connections));
  const many = groups.length > PAGE_SIZE;
  const filtered = groups
    .filter((g) => g.key.toLowerCase().includes(query.trim().toLowerCase()))
    .sort((a, b) =>
      order === "name"
        ? a.key.localeCompare(b.key, undefined, { numeric: true })
        : order === "most"
          ? b.connections - a.connections
          : a.connections - b.connections,
    );
  const pages = Math.max(1, Math.ceil(filtered.length / PAGE_SIZE));
  const current = Math.min(page, pages - 1);
  const shown = many
    ? filtered.slice(current * PAGE_SIZE, (current + 1) * PAGE_SIZE)
    : filtered;
  return (
    <div className="address-table">
      <h3>{title}</h3>
      {many && (
        <div className="balance-tools">
          <input
            type="search"
            aria-label={`Filter ${title.toLowerCase()}`}
            placeholder="Filter by name"
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
              setPage(0);
            }}
          />
          <select
            aria-label={`Sort ${title.toLowerCase()}`}
            value={order}
            onChange={(e) => {
              setOrder(e.target.value as Order);
              setPage(0);
            }}
          >
            <option value="most">Most connections</option>
            <option value="least">Fewest connections</option>
            <option value="name">Name</option>
          </select>
        </div>
      )}
      {shown.map((g) => (
        <div className="balance-row" key={g.key}>
          <span className="mono" title={g.key}>
            {g.key}
          </span>
          <progress max={max} value={g.connections} aria-label={g.key} />
          <span>{number(g.connections)}</span>
        </div>
      ))}
      {many && (
        <nav className="balance-pager" aria-label={`${title} pages`}>
          <span>
            {filtered.length
              ? `${number(current * PAGE_SIZE + 1)}–${number(current * PAGE_SIZE + shown.length)} of ${number(filtered.length)}`
              : "No matches"}
          </span>
          <button disabled={current === 0} onClick={() => setPage(current - 1)}>
            Previous
          </button>
          <span className="mono">
            {current + 1} / {pages}
          </span>
          <button
            disabled={current >= pages - 1}
            onClick={() => setPage(current + 1)}
          >
            Next
          </button>
        </nav>
      )}
    </div>
  );
}

const sumOf = (g: BalanceGroup[]) => g.reduce((a, x) => a + x.connections, 0);
const pct = (v: number) => `${(v * 100).toFixed(1)}%`;
const ratio = (v: number | null) => (v == null ? "∞" : v.toFixed(2));

// liveBalance derives a balance from the newest non-baseline sample while a
// test runs; finished tests carry the server-computed peak in Test.balance.
export function liveBalance(
  samples: FleetSample[] | undefined,
): Balance | undefined {
  const s = [...(samples || [])].reverse().find((x) => !x.baseline);
  if (!s) return undefined;
  const sum = (m: Map<string, number>) =>
    [...m].map(([key, connections]) => ({ key, connections }));
  const targets = new Map<string, number>();
  const zones = new Map<string, number>();
  let allZones = true;
  for (const t of s.targets) {
    if (t.error) continue;
    targets.set(t.target, (targets.get(t.target) || 0) + t.connections);
    if (t.zone) zones.set(t.zone, (zones.get(t.zone) || 0) + t.connections);
    else allZones = false;
  }
  const stats = (g: BalanceGroup[]) => {
    const v = g.map((x) => x.connections);
    const mean = v.reduce((a, b) => a + b, 0) / (v.length || 1);
    const lo = Math.min(...v);
    const hi = Math.max(...v);
    const sd = Math.sqrt(
      v.reduce((a, b) => a + (b - mean) ** 2, 0) / (v.length || 1),
    );
    return {
      mean,
      sd,
      lo,
      hi,
      ratio: hi === 0 ? 1 : lo === 0 ? null : hi / lo,
    };
  };
  const t = sum(targets);
  const z = allZones ? sum(zones) : [];
  const ts = stats(t);
  const zs = stats(z);
  return {
    timestamp: s.timestamp,
    complete: s.complete,
    targets: t,
    zones: z,
    targetCV: ts.mean > 0 ? ts.sd / ts.mean : 0,
    targetRatio: t.length ? ts.ratio : 1,
    zoneSkew: zs.mean > 0 ? (zs.hi - zs.lo) / zs.mean : 0,
    zoneRatio: z.length ? zs.ratio : 1,
  };
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="balance-stat">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

export function BalancePanel({ balance }: { balance?: Balance }) {
  if (!balance) return null;
  const hasZones = balance.zones.length > 0;
  const observed = sumOf(balance.targets) > 0;
  const cvSeen = balance.targets.length >= 2 && observed;
  return (
    <section className="panel balance-panel">
      <div className="panel-heading">
        <div>
          <h2>Broker balance</h2>
          <p>Connections per broker target and zone, from broker telemetry</p>
        </div>
      </div>
      {!balance.complete && (
        <div className="notice warning" role="status">
          <p>
            Incomplete telemetry: some broker targets did not report, so balance
            figures may be off.
          </p>
        </div>
      )}
      <div className="balance-stats">
        <Stat label="Targets" value={number(balance.targets.length)} />
        {cvSeen && <Stat label="CV" value={pct(balance.targetCV)} />}
        {cvSeen && <Stat label="Max/min" value={ratio(balance.targetRatio)} />}
        {hasZones && (
          <Stat
            label="Zone skew"
            value={
              sumOf(balance.zones) > 0 && balance.zones.length >= 2
                ? pct(balance.zoneSkew)
                : "—"
            }
          />
        )}
        {!observed ? (
          <p className="balance-note">No observations</p>
        ) : (
          !cvSeen && (
            <p className="balance-note">
              Spread statistics need at least two targets.
            </p>
          )
        )}
      </div>
      <Bars title="Connections per target" groups={balance.targets} />
      {hasZones ? (
        <Bars title="Connections per zone" groups={balance.zones} />
      ) : (
        <p className="balance-note balance-zones-missing">
          Zone labels missing on one or more targets; zone statistics are
          unavailable.
        </p>
      )}
    </section>
  );
}
