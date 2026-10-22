import type { LabelCount, Sample, Snapshot } from "./model";
import { number } from "./model";

const MAX_BARS = 60;
const palette = [
  "#4f8cff",
  "#f5a524",
  "#2fb67c",
  "#d6489c",
  "#8b5cf6",
  "#14b8c4",
  "#e5584f",
  "#9aa3b2",
];

function share(active: number, total: number) {
  return total > 0 ? `${((active / total) * 100).toFixed(1)}%` : "—";
}

function CountTable({
  title,
  rows,
  showLabel,
  showShare,
  connectsHeader,
}: {
  title: string;
  rows: LabelCount[];
  showLabel?: boolean;
  showShare?: boolean;
  connectsHeader?: string;
}) {
  const total = rows.reduce((sum, r) => sum + Math.max(0, r.active), 0);
  return (
    <div className="address-table">
      <h3>{title}</h3>
      <div className="address-table-scroll">
        <table>
          <thead>
            <tr>
              <th>{showLabel ? "Address" : "Key"}</th>
              {showLabel && <th>Label</th>}
              {showShare && <th>Active</th>}
              <th>{connectsHeader || "Connects"}</th>
              {showShare && <th>Share of active</th>}
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.key}>
                <td className="mono">{r.key}</td>
                {showLabel && <td>{r.label || "—"}</td>}
                {showShare && <td>{number(r.active)}</td>}
                <td>{number(r.connects)}</td>
                {showShare && <td>{share(r.active, total)}</td>}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function StackedBars({ samples, keys }: { samples: Sample[]; keys: string[] }) {
  const recent = samples.slice(-MAX_BARS);
  if (recent.length < 2 || keys.length === 0) return null;
  return (
    <div className="address-stack-block">
      <div
        className="address-stack"
        role="img"
        aria-label="Active connections by label per sample"
        style={{ display: "flex", alignItems: "flex-end", gap: 2, height: 80 }}
      >
        {recent.map((sample) => {
          const rows = sample.snapshot.addressLabels || [];
          const total = rows.reduce((sum, r) => sum + Math.max(0, r.active), 0);
          return (
            <div
              key={sample.timestamp}
              title={rows.map((r) => `${r.key}: ${r.active}`).join(", ")}
              style={{
                flex: 1,
                height: "100%",
                display: "flex",
                flexDirection: "column-reverse",
              }}
            >
              {keys.map((k, i) => {
                const active = rows.find((r) => r.key === k)?.active || 0;
                return (
                  <div
                    key={k}
                    style={{
                      height: `${total > 0 ? (Math.max(0, active) / total) * 100 : 0}%`,
                      background: palette[i % palette.length],
                    }}
                  />
                );
              })}
            </div>
          );
        })}
      </div>
      <p className="subdued">
        {keys.map((k, i) => (
          <span key={k} style={{ marginRight: 12 }}>
            <i
              style={{
                display: "inline-block",
                width: 8,
                height: 8,
                marginRight: 4,
                background: palette[i % palette.length],
              }}
            />
            {k}
          </span>
        ))}
      </p>
    </div>
  );
}

export function AddressDistribution({
  snapshot: s,
  samples = [],
}: {
  snapshot: Snapshot;
  samples?: Sample[];
}) {
  const labels = s.addressLabels || [];
  const addresses = s.remoteAddresses || [];
  const dnsAnswers = s.dnsAnswers || [];
  const dnsErrors = s.dnsErrors || [];
  if (
    !labels.length &&
    !addresses.length &&
    !dnsAnswers.length &&
    !dnsErrors.length
  )
    return null;
  const keys = [
    ...new Set(
      [s, ...samples.map((x) => x.snapshot)].flatMap((x) =>
        (x.addressLabels || []).map((r) => r.key),
      ),
    ),
  ].sort();
  return (
    <section className="panel address-distribution">
      <div className="panel-heading">
        <div>
          <h2>Broker address distribution</h2>
          <p>Where connections landed, by label and by address</p>
        </div>
      </div>
      {labels.length > 0 && (
        <CountTable title="By label" rows={labels} showShare />
      )}
      {labels.length > 0 && <StackedBars samples={samples} keys={keys} />}
      {addresses.length > 0 && (
        <CountTable title="By address" rows={addresses} showLabel showShare />
      )}
      {dnsAnswers.length > 0 && (
        <CountTable
          title="DNS answers"
          rows={dnsAnswers}
          connectsHeader="Times returned"
        />
      )}
      {dnsErrors.length > 0 && (
        <CountTable
          title="DNS errors"
          rows={dnsErrors}
          connectsHeader="Failed attempts"
        />
      )}
    </section>
  );
}
