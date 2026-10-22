import type { Balance, BalanceGroup, FleetSample } from "./model";
import { number } from "./model";

function Bars({ title, groups }: { title: string; groups: BalanceGroup[] }) {
  const max = Math.max(1, ...groups.map((g) => g.connections));
  return (
    <div className="address-table">
      <h3>{title}</h3>
      {groups.map((g) => (
        <div className="balance-row" key={g.key}>
          <span className="mono">{g.key}</span>
          <progress max={max} value={g.connections} aria-label={g.key} />
          <span>{number(g.connections)}</span>
        </div>
      ))}
    </div>
  );
}

const sumOf = (g: BalanceGroup[]) => g.reduce((a, x) => a + x.connections, 0);
const pct = (v: number) => `${(v * 100).toFixed(1)}%`;
const ratio = (v: number | null) => (v == null ? "∞" : v.toFixed(2));

// liveBalance derives a balance from the newest non-baseline sample while a
// test runs; finished tests carry the server-computed peak in Test.balance.
export function liveBalance(samples: FleetSample[] | undefined): Balance | undefined {
  const s = [...(samples || [])].reverse().find((x) => !x.baseline);
  if (!s) return undefined;
  const sum = (m: Map<string, number>) => [...m].map(([key, connections]) => ({ key, connections }));
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
    const sd = Math.sqrt(v.reduce((a, b) => a + (b - mean) ** 2, 0) / (v.length || 1));
    return { mean, sd, lo, hi, ratio: hi === 0 ? 1 : lo === 0 ? null : hi / lo };
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
          <p>Incomplete telemetry: some broker targets did not report, so balance figures may be off.</p>
        </div>
      )}
      <div className="balance-stats">
        <Stat label="Targets" value={number(balance.targets.length)} />
        {cvSeen && <Stat label="CV" value={pct(balance.targetCV)} />}
        {cvSeen && <Stat label="Max/min" value={ratio(balance.targetRatio)} />}
        {hasZones && (
          <Stat
            label="Zone skew"
            value={sumOf(balance.zones) > 0 && balance.zones.length >= 2 ? pct(balance.zoneSkew) : "—"}
          />
        )}
        {!observed ? (
          <p className="balance-note">No observations</p>
        ) : (
          !cvSeen && <p className="balance-note">Spread statistics need at least two targets.</p>
        )}
      </div>
      <Bars title="Connections per target" groups={balance.targets} />
      {hasZones ? (
        <Bars title="Connections per zone" groups={balance.zones} />
      ) : (
        <p className="balance-note balance-zones-missing">
          Zone labels missing on one or more targets; zone statistics are unavailable.
        </p>
      )}
    </section>
  );
}
