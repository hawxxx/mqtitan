import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useQuery, useQueryClient, useMutation } from "@tanstack/react-query";
import { LatencyPanel } from "./LatencyPanel";
import { CertificateSettings } from "./CertificateSettings";
import { LiveLoadControls } from "./LiveLoadControls";
import { GridPattern } from "./components/magicui/GridPattern";
import { RunAgainButton } from "./RunAgainButton";
import {
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
  Link,
  useNavigate,
  useRouterState,
} from "@tanstack/react-router";
import {
  useReactTable,
  getCoreRowModel,
  getSortedRowModel,
  flexRender,
  type ColumnDef,
  type SortingState,
} from "@tanstack/react-table";
import { useVirtualizer } from "@tanstack/react-virtual";
import {
  Activity,
  ArrowUpRight,
  Box,
  CheckCircle2,
  ChevronRight,
  CircleHelp,
  Clock,
  Database,
  Download,
  FileCode2,
  Gauge,
  GitCompareArrows,
  Layers,
  LayoutDashboard,
  Play,
  Plus,
  Radio,
  RefreshCw,
  Search,
  Server,
  Settings,
  ShieldCheck,
  Square,
  Sun,
  Terminal,
  TriangleAlert,
  X,
  Zap,
} from "lucide-react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import {
  bytes,
  download,
  headers,
  latency,
  makeScenario,
  number,
  request,
  type Check,
  type Sample,
  type SavedScenario,
  type Test,
  type Worker,
  type WizardValues,
} from "./model";

const nav = [
  ["Dashboard", "/", LayoutDashboard],
  ["Tests", "/tests", Activity],
  ["Scenarios", "/scenarios", FileCode2],
  ["Brokers", "/brokers", Database],
  ["Workers", "/workers", Server],
  ["Compare", "/compare", GitCompareArrows],
  ["Errors", "/errors", TriangleAlert],
  ["Settings", "/settings", Settings],
] as const;
function useTests() {
  return useQuery({
    queryKey: ["tests"],
    queryFn: () => request<Test[]>("/tests"),
    refetchInterval: 3000,
  });
}
function Empty({
  title,
  detail,
  action,
}: {
  title: string;
  detail: string;
  action?: ReactNode;
}) {
  return (
    <div className="empty">
      <div className="empty-mark">
        <Activity size={30} />
      </div>
      <h3>{title}</h3>
      <p>{detail}</p>
      {action}
    </div>
  );
}
function ErrorNotice({ error, retry }: { error: unknown; retry?: () => void }) {
  return (
    <div className="notice error" role="alert">
      <TriangleAlert size={18} />
      <div>
        <strong>Unable to complete request</strong>
        <p>{error instanceof Error ? error.message : String(error)}</p>
      </div>
      {retry && (
        <button onClick={retry}>
          <RefreshCw size={14} /> Retry
        </button>
      )}
    </div>
  );
}
function Badge({ status }: { status: string }) {
  return (
    <span className={`badge ${status.toLowerCase()}`}>
      <i />
      {status}
    </span>
  );
}
function Metric({
  label,
  value,
  sub,
  accent = false,
}: {
  label: string;
  value: ReactNode;
  sub?: string;
  accent?: boolean;
}) {
  return (
    <div className={`metric ${accent ? "accent" : ""}`}>
      <span>{label}</span>
      <strong>{value}</strong>
      <small>{sub}</small>
    </div>
  );
}
function Title({
  eyebrow,
  title,
  detail,
  children,
}: {
  eyebrow: string;
  title: string;
  detail: string;
  children?: ReactNode;
}) {
  return (
    <div className="page-title">
      <div>
        <div className="eyebrow">{eyebrow}</div>
        <h1>{title}</h1>
        <p>{detail}</p>
      </div>
      <div className="actions">{children}</div>
    </div>
  );
}
function Chart({
  samples,
  field = "messageRate",
  label = "Messages / second",
}: {
  samples: Sample[];
  field?: "messageRate" | "byteRate" | "connected" | "p95";
  label?: string;
}) {
  const host = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!host.current || samples.length < 2) return;
    const values = samples.map((s) =>
      field === "connected"
        ? s.snapshot.connected
        : field === "p95"
          ? s.snapshot.p95 / 1e6
          : s[field],
    );
    const plot = new uPlot(
      {
        width: host.current.clientWidth,
        height: 235,
        series: [
          {},
          { label, stroke: "#39c7b0", width: 2, fill: "rgba(57,199,176,.09)" },
        ],
        axes: [
          {
            stroke: "#7f949c",
            grid: { stroke: "rgba(127,148,156,.08)" },
            font: "11px monospace",
          },
          {
            stroke: "#7f949c",
            grid: { stroke: "rgba(127,148,156,.1)" },
            font: "11px monospace",
          },
        ],
        cursor: { drag: { x: true, y: false } },
        legend: { show: false },
      },
      [samples.map((s) => Date.parse(s.timestamp) / 1000), values],
      host.current,
    );
    const resize = new ResizeObserver(() => {
      if (host.current)
        plot.setSize({ width: host.current.clientWidth, height: 235 });
    });
    resize.observe(host.current);
    return () => {
      resize.disconnect();
      plot.destroy();
    };
  }, [samples, field, label]);
  return (
    <div className="chart-body">
      {samples.length < 2 ? (
        <div className="chart-empty">
          <Activity size={25} />
          <span>
            {samples.length
              ? "Collecting more samples…"
              : "No recorded samples"}
          </span>
          <small>Metrics appear after a test begins.</small>
        </div>
      ) : (
        <div ref={host} />
      )}
    </div>
  );
}
function RunTable({
  tests,
  open,
}: {
  tests: Test[];
  open: (id: string) => void;
}) {
  const [search, setSearch] = useState("");
  const [sorting, setSorting] = useState<SortingState>([
    { id: "startedAt", desc: true },
  ]);
  const data = useMemo(
    () =>
      tests.filter((t) =>
        `${t.name} ${t.id} ${t.status}`
          .toLowerCase()
          .includes(search.toLowerCase()),
      ),
    [tests, search],
  );
  const columns = useMemo<ColumnDef<Test>[]>(
    () => [
      {
        accessorKey: "name",
        header: "Test name",
        cell: (c) => (
          <button
            className="table-name"
            onClick={() => open(c.row.original.id)}
          >
            <span className="run-icon">
              <Activity size={15} />
            </span>
            <span>
              {c.getValue<string>()}
              <small>{c.row.original.id.slice(0, 12)}</small>
            </span>
          </button>
        ),
      },
      {
        accessorKey: "status",
        header: "Status",
        cell: (c) => <Badge status={c.getValue<string>()} />,
      },
      {
        accessorKey: "startedAt",
        header: "Started",
        cell: (c) => (
          <span className="mono subdued">
            {new Date(c.getValue<string>()).toLocaleString()}
          </span>
        ),
      },
      {
        id: "clients",
        header: "Peak clients",
        accessorFn: (t) => t.snapshot.peakConnected,
        cell: (c) => (
          <span className="mono">{number(c.getValue<number>())}</span>
        ),
      },
      {
        id: "messages",
        header: "Published",
        accessorFn: (t) => t.snapshot.published,
        cell: (c) => (
          <span className="mono">{number(c.getValue<number>())}</span>
        ),
      },
      {
        id: "p95",
        header: "P95 latency",
        accessorFn: (t) => t.snapshot.p95,
        cell: (c) => (
          <span className="mono">{latency(c.getValue<number>())}</span>
        ),
      },
    ],
    [open],
  );
  const table = useReactTable({
    data,
    columns,
    state: { sorting },
    onSortingChange: setSorting,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
  });
  const parent = useRef<HTMLDivElement>(null);
  const rows = table.getRowModel().rows;
  const virtual = useVirtualizer({
    count: rows.length,
    getScrollElement: () => parent.current,
    estimateSize: () => 66,
    overscan: 6,
  });
  return (
    <section className="panel">
      <div className="panel-heading">
        <h2>
          Test history <span className="count">{tests.length}</span>
        </h2>
        <label className="search">
          <Search size={15} />
          <input
            aria-label="Search tests"
            placeholder="Search tests…"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </label>
      </div>
      <div className="table-scroll" ref={parent}>
        <table>
          <thead>
            {table.getHeaderGroups().map((g) => (
              <tr key={g.id}>
                {g.headers.map((h) => (
                  <th key={h.id}>
                    <button onClick={h.column.getToggleSortingHandler()}>
                      {flexRender(h.column.columnDef.header, h.getContext())}
                      {h.column.getIsSorted() === "desc"
                        ? " ↓"
                        : h.column.getIsSorted() === "asc"
                          ? " ↑"
                          : ""}
                    </button>
                  </th>
                ))}
              </tr>
            ))}
          </thead>
          <tbody>
            {rows.length > 0 && (
              <tr
                style={{ height: virtual.getVirtualItems()[0]?.start || 0 }}
              />
            )}
            {virtual.getVirtualItems().map((v) => {
              const r = rows[v.index];
              return (
                <tr key={r.id}>
                  {r.getVisibleCells().map((c) => (
                    <td key={c.id}>
                      {flexRender(c.column.columnDef.cell, c.getContext())}
                    </td>
                  ))}
                </tr>
              );
            })}
            {rows.length > 0 && (
              <tr
                style={{
                  height:
                    virtual.getTotalSize() -
                    (virtual.getVirtualItems().at(-1)?.end || 0),
                }}
              />
            )}
          </tbody>
        </table>
        {!rows.length && (
          <Empty
            title="No tests found"
            detail={
              search
                ? "Try a different search."
                : "Start a test to record your first result."
            }
          />
        )}
      </div>
    </section>
  );
}
export function Dashboard({
  tests,
  open,
  create,
}: {
  tests: Test[];
  open: (id: string) => void;
  create: () => void;
}) {
  const running = tests.filter((t) => t.status === "running");
  const latest = running[0] || tests[0];
  const samples = useQuery({
    queryKey: ["metrics", latest?.id],
    queryFn: () => request<Sample[]>(`/tests/${latest!.id}/metrics`),
    enabled: !!latest,
    refetchInterval: latest?.status === "running" ? 3000 : false,
  });
  return (
    <>
      <div className="dashboard-intro">
        <GridPattern
          squares={[
            [12, 1],
            [15, 3],
            [20, 0],
            [24, 2],
          ]}
        />
        <Title
          eyebrow="OPERATIONS / OVERVIEW"
          title="Performance at a glance"
          detail="Your MQTT infrastructure. Measured, not guessed."
        >
          <button className="primary" onClick={create}>
            <Plus size={16} /> New test
          </button>
        </Title>
      </div>
      <div className="metrics">
        <Metric
          label="ACTIVE TESTS"
          value={running.length}
          sub={`${tests.length} total recorded runs`}
          accent
        />
        <Metric
          label="CONNECTED CLIENTS"
          value={number(running.reduce((n, t) => n + t.snapshot.connected, 0))}
          sub="Across active tests"
        />
        <Metric
          label="MESSAGES PUBLISHED"
          value={number(tests.reduce((n, t) => n + t.snapshot.published, 0))}
          sub="All recorded runs"
        />
        <Metric
          label="LATEST P95 LATENCY"
          value={latest ? latency(latest.snapshot.p95) : "—"}
          sub={latest ? latest.name : "Waiting for first result"}
        />
      </div>
      {!tests.length ? (
        <section className="panel welcome">
          <GridPattern className="welcome-grid" />
          <Empty
            title="Your performance baseline starts here"
            detail="Connect a broker, define your workload, and see how your MQTT infrastructure behaves under pressure."
            action={
              <button className="primary" onClick={create}>
                <Play size={15} /> Create your first test
              </button>
            }
          />
          <div className="welcome-footer">
            <span>
              <ShieldCheck size={17} /> MQTT 3.1.1 & 5.0
            </span>
            <span>
              <Zap size={17} /> Real-time telemetry
            </span>
            <span>
              <FileCode2 size={17} /> Reproducible YAML scenarios
            </span>
          </div>
        </section>
      ) : (
        <section className="panel">
          <div className="panel-heading">
            <div>
              <h2>Message throughput</h2>
              <p>{latest?.name} · most recent run</p>
            </div>
            <span className="chart-key">
              <i /> Published / sec
            </span>
          </div>
          {samples.error ? (
            <ErrorNotice error={samples.error} />
          ) : (
            <Chart samples={samples.data || []} />
          )}
          <div className="panel-footer">
            <span>Actual samples from the load engine</span>
            <button className="text-button" onClick={() => open(latest!.id)}>
              Inspect run <ArrowUpRight size={14} />
            </button>
          </div>
        </section>
      )}
      <div className="section-label">RECENT ACTIVITY</div>
      <RunTable tests={tests.slice(0, 20)} open={open} />
      <div className="help-strip">
        <Terminal size={19} />
        <div>
          <strong>Built for reproducible performance.</strong>
          <span>
            Run the same YAML scenarios from the CLI and compare results here.
          </span>
        </div>
        <code>mqtitan run scenario.yaml</code>
      </div>
    </>
  );
}
export function Wizard({
  done,
  close,
}: {
  done: (yaml: string) => void;
  close: () => void;
}) {
  const [v, setV] = useState<WizardValues>({
    name: "MQTT baseline",
    broker: "mqtt://localhost:1883",
    clients: 100,
    rate: 1,
    duration: 60,
    qos: 1,
  });
  const [error, setError] = useState("");
  const [certificateBusy, setCertificateBusy] = useState(false);
  const profiles = readStorage<
    { name: string; url: string; version?: string }[]
  >("mqtitan.brokers", []);
  function field(k: keyof WizardValues, value: string) {
    if (k === "broker" && !/^(mqtts|wss):\/\//.test(value))
      setCertificateBusy(false);
    setV((p) => ({
      ...p,
      [k]: ["clients", "capacity", "rate", "duration", "qos"].includes(k)
        ? Number(value)
        : value,
    }));
  }
  return (
    <div className="modal-backdrop">
      <section
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="wizard-title"
      >
        <div className="modal-heading">
          <div>
            <span className="eyebrow">QUICK START</span>
            <h2 id="wizard-title">Build a test</h2>
          </div>
          <button aria-label="Close wizard" onClick={close}>
            <X size={18} />
          </button>
        </div>
        <p className="subdued">
          Define a publishing workload. Review the generated scenario before
          launching.
        </p>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            try {
              done(makeScenario(v));
            } catch (err) {
              setError((err as Error).message);
            }
          }}
        >
          <label>
            Test name
            <input
              value={v.name}
              onChange={(e) => field("name", e.target.value)}
              required
            />
          </label>
          {profiles.length > 0 && (
            <label>
              Broker profile
              <select
                onChange={(e) => {
                  const p = profiles[Number(e.target.value)];
                  if (p)
                    setV((x) => ({ ...x, broker: p.url, version: p.version }));
                }}
              >
                <option value="">Custom broker</option>
                {profiles.map((p, i) => (
                  <option key={i} value={i}>
                    {p.name}
                  </option>
                ))}
              </select>
            </label>
          )}
          <label>
            Broker URL
            <input
              value={v.broker}
              onChange={(e) => field("broker", e.target.value)}
              required
            />
          </label>
          <div className="form-grid">
            <label>
              Clients
              <input
                type="number"
                min="1"
                max="1000000"
                value={v.clients}
                onChange={(e) => field("clients", e.target.value)}
              />
            </label>
            <label>
              Messages / client / sec
              <input
                type="number"
                min="0.01"
                step="any"
                value={v.rate}
                onChange={(e) => field("rate", e.target.value)}
              />
            </label>
            <label>
              Maximum clients (live controls)
              <input
                type="number"
                min={v.clients}
                max="1000000"
                value={v.capacity ?? v.clients}
                onChange={(e) => field("capacity", e.target.value)}
              />
              <span className="subdued">
                Set higher than Clients to allow live increases.
              </span>
            </label>
            <label>
              Duration (seconds)
              <input
                type="number"
                min="1"
                value={v.duration}
                onChange={(e) => field("duration", e.target.value)}
              />
            </label>
            <label>
              Quality of service
              <select
                value={v.qos}
                onChange={(e) => field("qos", e.target.value)}
              >
                <option value="0">QoS 0 · At most once</option>
                <option value="1">QoS 1 · At least once</option>
                <option value="2">QoS 2 · Exactly once</option>
              </select>
            </label>
            <label>
              Username (optional)
              <input
                autoComplete="username"
                value={v.username || ""}
                onChange={(e) => field("username", e.target.value)}
              />
            </label>
            <label>
              Password (optional)
              <input
                type="password"
                autoComplete="current-password"
                value={v.password || ""}
                onChange={(e) => field("password", e.target.value)}
              />
            </label>
          </div>
          {/^(mqtts|wss):\/\//.test(v.broker) && (
            <CertificateSettings
              value={v.tls || {}}
              onChange={(tls) => setV((previous) => ({ ...previous, tls }))}
              onBusyChange={setCertificateBusy}
            />
          )}
          <div className="estimate">
            <Zap size={16} />
            <span>
              Target throughput{" "}
              <strong>{number(v.clients * v.rate)} msg/s</strong>
            </span>
            <span>
              Payload <strong>256 bytes</strong>
            </span>
          </div>
          {error && (
            <div className="notice error" role="alert">
              {error}
            </div>
          )}
          <div className="modal-footer">
            <button type="button" onClick={close}>
              Cancel
            </button>
            <button
              className="primary"
              type="submit"
              disabled={certificateBusy}
            >
              Review scenario <ChevronRight size={16} />
            </button>
          </div>
        </form>
      </section>
    </div>
  );
}
const defaultYAML = makeScenario({
  name: "MQTT baseline",
  broker: "mqtt://localhost:1883",
  clients: 100,
  rate: 1,
  duration: 60,
  qos: 1,
});
export function ScenarioEditor({
  initial,
  onStarted,
}: {
  initial?: string;
  onStarted: (id: string) => void;
}) {
  const [yaml, setYaml] = useState(initial || defaultYAML);
  const [name, setName] = useState("MQTT baseline");
  const [message, setMessage] = useState("");
  const [selectedWorkers, setSelectedWorkers] = useState<string[]>([]);
  const qc = useQueryClient();
  const workers = useQuery({
    queryKey: ["workers"],
    queryFn: () => request<Worker[]>("/workers"),
  });
  const saved = useQuery({
    queryKey: ["scenarios"],
    queryFn: () => request<SavedScenario[]>("/scenarios"),
  });
  const start = useMutation({
    mutationFn: () =>
      request<Test>("/tests", {
        method: "POST",
        body: JSON.stringify({
          scenario: yaml,
          ...(selectedWorkers.length ? { workers: selectedWorkers } : {}),
        }),
      }),
    onSuccess: (t) => {
      qc.invalidateQueries({ queryKey: ["tests"] });
      onStarted(t.id);
    },
  });
  const save = useMutation({
    mutationFn: () =>
      request<SavedScenario>("/scenarios", {
        method: "POST",
        body: JSON.stringify({ name, yaml }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["scenarios"] });
      setMessage("Scenario saved.");
    },
  });
  const plan = useMutation({
    mutationFn: () =>
      request<Record<string, unknown>>("/plan", {
        method: "POST",
        body: JSON.stringify({ scenario: yaml }),
      }),
  });
  useEffect(() => {
    if (initial) setYaml(initial);
  }, [initial]);
  return (
    <>
      <Title
        eyebrow="WORKLOADS / SCENARIOS"
        title="Scenario lab"
        detail="Versionable workloads. Explicit stages. Repeatable results."
      >
        <button disabled={plan.isPending} onClick={() => plan.mutate()}>
          <Gauge size={16} /> Validate & plan
        </button>
        <button
          className="primary"
          disabled={start.isPending}
          onClick={() => start.mutate()}
        >
          <Play size={15} />
          {start.isPending ? "Starting…" : "Start test"}
        </button>
      </Title>
      {(start.error || save.error || plan.error) && (
        <ErrorNotice error={start.error || save.error || plan.error} />
      )}
      <div className="editor-layout">
        <section className="panel editor">
          <div className="panel-heading">
            <h2>
              <FileCode2 size={17} /> scenario.yaml
            </h2>
            <span className="subdued mono">YAML / v1alpha1</span>
          </div>
          <textarea
            aria-label="Scenario YAML"
            spellCheck={false}
            value={yaml}
            onChange={(e) => setYaml(e.target.value)}
          />
          <div className="panel-footer">
            <span>Broker credentials in YAML are sent to the server.</span>
            <span className="mono">{yaml.split("\n").length} lines</span>
          </div>
        </section>
        <aside>
          <section className="panel side-panel">
            <h2>
              <Server size={17} /> Execution workers
            </h2>
            <p>
              Use the local engine, or select remote workers for a distributed
              run.
            </p>
            {workers.error ? (
              <ErrorNotice error={workers.error} />
            ) : (
              workers.data
                ?.filter((w) => w.id !== "local")
                .map((w) => (
                  <label className="worker-choice" key={w.id}>
                    <input
                      type="checkbox"
                      aria-label={w.id}
                      checked={selectedWorkers.includes(w.id)}
                      disabled={
                        !["ready", "online", "idle", "active"].includes(
                          w.status,
                        )
                      }
                      onChange={(e) =>
                        setSelectedWorkers((old) =>
                          e.target.checked
                            ? [...old, w.id]
                            : old.filter((x) => x !== w.id),
                        )
                      }
                    />
                    <span>
                      {w.id}
                      <small>
                        {w.status} · {w.cpuCount} cores
                      </small>
                    </span>
                  </label>
                ))
            )}
            <small className="subdued">
              {selectedWorkers.length
                ? `${selectedWorkers.length} selected workers`
                : "Local engine selected"}
            </small>
          </section>
          <section className="panel side-panel">
            <h2>Save to your library</h2>
            <p>Keep a workload ready for your next benchmark.</p>
            <label>
              Scenario name
              <input value={name} onChange={(e) => setName(e.target.value)} />
            </label>
            <button
              disabled={save.isPending || !name.trim()}
              onClick={() => save.mutate()}
            >
              <Plus size={15} /> Save scenario
            </button>
            {message && (
              <p className="success" role="status">
                {message}
              </p>
            )}
          </section>
          <section className="panel side-panel">
            <h2>
              Saved scenarios{" "}
              <span className="count">{saved.data?.length || 0}</span>
            </h2>
            {saved.error ? (
              <ErrorNotice error={saved.error} />
            ) : saved.data?.length ? (
              saved.data.map((s) => (
                <button
                  className="scenario-item"
                  key={s.id}
                  onClick={() => {
                    setYaml(s.yaml);
                    setName(s.name);
                  }}
                >
                  <FileCode2 size={17} />
                  <span>
                    {s.name}
                    <small>{new Date(s.createdAt).toLocaleDateString()}</small>
                  </span>
                  <ChevronRight size={14} />
                </button>
              ))
            ) : (
              <p>No saved scenarios yet.</p>
            )}
          </section>
          {plan.data && (
            <section className="panel side-panel">
              <h2>
                <CheckCircle2 size={17} /> Validated execution plan
              </h2>
              {Object.entries(plan.data)
                .filter(([k]) => k !== "checks")
                .map(([k, v]) => (
                  <div className="plan-row" key={k}>
                    <span>{k.replace(/([A-Z])/g, " $1").trim()}</span>
                    <strong className="mono">{String(v)}</strong>
                  </div>
                ))}
              {Array.isArray(plan.data.checks) &&
                plan.data.checks.map((c: Check) => (
                  <div
                    className={`capacity-check ${c.Status.toLowerCase()}`}
                    key={c.Name}
                  >
                    <strong>
                      {c.Status} · {c.Name}
                    </strong>
                    <p>{c.Value}</p>
                    {c.Recommendation && <small>{c.Recommendation}</small>}
                  </div>
                ))}
            </section>
          )}
        </aside>
      </div>
    </>
  );
}
function useStream(id: string, running: boolean) {
  const [state, setState] = useState("connecting");
  const qc = useQueryClient();
  useEffect(() => {
    if (!running) {
      setState("recorded");
      return;
    }
    const abort = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    async function connect() {
      try {
        setState("connecting");
        const r = await fetch(
          `/api/v1/tests/${encodeURIComponent(id)}/stream`,
          { headers: headers(), signal: abort.signal },
        );
        if (!r.ok || !r.body) throw new Error("Stream unavailable");
        setState("live");
        const reader = r.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        while (!abort.signal.aborted) {
          const chunk = await reader.read();
          if (chunk.done) break;
          buffer += decoder
            .decode(chunk.value, { stream: true })
            .replace(/\r\n/g, "\n");
          let end;
          while ((end = buffer.indexOf("\n\n")) >= 0) {
            const frame = buffer.slice(0, end);
            buffer = buffer.slice(end + 2);
            const event = frame
              .split("\n")
              .find((l) => l.startsWith("event:"))
              ?.slice(6)
              .trim();
            const data = frame
              .split("\n")
              .filter((l) => l.startsWith("data:"))
              .map((l) => l.slice(5).trim())
              .join("\n");
            if (!data) continue;
            try {
              const v = JSON.parse(data);
              if (event === "metrics")
                qc.setQueryData<Sample[]>(["metrics", id], (old) =>
                  [...(old || []), v].slice(-10000),
                );
              if (event === "status") {
                qc.setQueryData(["test", id], v);
                qc.invalidateQueries({ queryKey: ["tests"] });
              }
            } catch {
              /* Invalid frames are followed by periodic API reconciliation. */
            }
          }
        }
        if (!abort.signal.aborted) {
          setState("reconnecting");
          timer = setTimeout(connect, 2000);
        }
      } catch {
        if (!abort.signal.aborted) {
          setState("disconnected");
          timer = setTimeout(connect, 3000);
        }
      }
    }
    connect();
    return () => {
      abort.abort();
      clearTimeout(timer);
    };
  }, [id, running, qc]);
  return state;
}
function TestDetail({ id }: { id: string }) {
  const navigate = useNavigate();
  const test = useQuery({
    queryKey: ["test", id],
    queryFn: () => request<Test>(`/tests/${id}`),
    refetchInterval: 2000,
  });
  const t = test.data;
  const metrics = useQuery({
    queryKey: ["metrics", id],
    queryFn: () => request<Sample[]>(`/tests/${id}/metrics`),
    refetchInterval: t?.status === "running" ? 5000 : false,
  });
  const stream = useStream(id, t?.status === "running");
  const [tab, setTab] = useState("Overview");
  const [exportError, setExportError] = useState<unknown>();
  const qc = useQueryClient();
  const stop = useMutation({
    mutationFn: () => request(`/tests/${id}/stop`, { method: "POST" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["test", id] });
      qc.invalidateQueries({ queryKey: ["tests"] });
    },
  });
  if (test.error)
    return <ErrorNotice error={test.error} retry={() => test.refetch()} />;
  if (!t)
    return (
      <div className="loading">
        <RefreshCw className="spin" /> Loading test…
      </div>
    );
  const s = t.snapshot;
  return (
    <>
      <Title
        eyebrow={`TESTS / ${id.slice(0, 12)}`}
        title={t.name}
        detail={`Started ${new Date(t.startedAt).toLocaleString()}${t.endedAt ? ` · Ended ${new Date(t.endedAt).toLocaleString()}` : ""}`}
      >
        <Badge status={t.status} />
        <RunAgainButton
          key={id}
          test={t}
          onStarted={(run) => {
            qc.setQueryData(["test", run.id], run);
            qc.invalidateQueries({ queryKey: ["tests"] });
            setTab("Overview");
            navigate({ to: "/tests/$id", params: { id: run.id } });
          }}
        />
        {t.status === "running" && (
          <button
            className="danger"
            disabled={stop.isPending}
            onClick={() => stop.mutate()}
          >
            <Square size={14} /> Stop test
          </button>
        )}
        <button onClick={() => download(id, "csv").catch(setExportError)}>
          <Download size={15} /> CSV
        </button>
        <button onClick={() => download(id, "json").catch(setExportError)}>
          <Download size={15} /> JSON
        </button>
      </Title>
      {t.status !== "running" && t.workerIds == null && (
        <p className="rerun-legacy-note">
          This older run has no recorded worker selection. Run again uses the
          local engine.
        </p>
      )}
      {t.sourceTestId && (
        <p className="rerun-source">
          New run of{" "}
          <Link to="/tests/$id" params={{ id: t.sourceTestId }}>
            test {t.sourceTestId.slice(0, 12)}
          </Link>
          . Original results are unchanged.
        </p>
      )}
      {(stop.error || exportError || t.error) && (
        <ErrorNotice error={stop.error || exportError || t.error} />
      )}
      <div className="tabs">
        {["Overview", "Latency", "Connections", "Thresholds", "Scenario"].map(
          (x) => (
            <button
              className={tab === x ? "active" : ""}
              key={x}
              onClick={() => setTab(x)}
            >
              {x}
            </button>
          ),
        )}
        <span className={`stream ${stream}`}>
          <i />
          {stream === "live"
            ? "Live stream"
            : stream === "recorded"
              ? "Recorded metrics"
              : `${stream} · polling active`}
        </span>
      </div>
      <div className="metrics">
        <Metric
          label="CONNECTED / TARGET"
          value={
            <>
              {number(s.connected)}
              <em> / {number(s.targetClients)}</em>
            </>
          }
          sub={`${number(s.peakConnected)} peak connected`}
          accent
        />
        <Metric
          label="PUBLISHED"
          value={number(s.published)}
          sub={`${number(s.received)} received`}
        />
        <Metric
          label="P95 LATENCY"
          value={latency(s.p95)}
          sub={`P99 ${latency(s.p99)}`}
        />
        <Metric
          label="TOTAL ERRORS"
          value={number(s.connectErrors + s.publishErrors)}
          sub={`${number(s.connectErrors)} connection · ${number(s.publishErrors)} publish`}
        />
      </div>
      {t.loadControl && (
        <LiveLoadControls
          test={t}
          onUpdated={(updated) => {
            qc.setQueryData(["test", id], updated);
            qc.invalidateQueries({ queryKey: ["tests"] });
          }}
        />
      )}
      {tab === "Scenario" ? (
        <section className="panel">
          <pre className="yaml-read">{t.scenario}</pre>
        </section>
      ) : tab === "Thresholds" ? (
        <section className="panel">
          <div className="panel-heading">
            <h2>Threshold evaluation</h2>
          </div>
          {t.thresholds?.length ? (
            <table>
              <thead>
                <tr>
                  <th>Metric</th>
                  <th>Expression</th>
                  <th>Observed</th>
                  <th>Result</th>
                </tr>
              </thead>
              <tbody>
                {t.thresholds.map((x) => (
                  <tr key={x.name}>
                    <td>{x.name}</td>
                    <td className="mono">{x.expression}</td>
                    <td className="mono">{x.observed}</td>
                    <td>
                      <Badge status={x.passed ? "passed" : "failed"} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          ) : (
            <Empty
              title="No thresholds configured"
              detail="Add thresholds to your scenario to evaluate pass or fail criteria."
            />
          )}
        </section>
      ) : (
        <>
          <section className="panel">
            <div className="panel-heading">
              <h2>
                {tab === "Latency"
                  ? "Publish latency · P95"
                  : tab === "Connections"
                    ? "Connected clients"
                    : "Message throughput"}
              </h2>
              <span className="chart-key">
                <i />{" "}
                {tab === "Latency"
                  ? "Milliseconds"
                  : tab === "Connections"
                    ? "Clients"
                    : "Messages / second"}
              </span>
            </div>
            {metrics.error ? (
              <ErrorNotice error={metrics.error} />
            ) : (
              <Chart
                samples={metrics.data || []}
                field={
                  tab === "Latency"
                    ? "p95"
                    : tab === "Connections"
                      ? "connected"
                      : "messageRate"
                }
              />
            )}
          </section>
          {tab === "Latency" && <LatencyPanel snapshot={s} />}
          <div className="two-col">
            <section className="panel side-panel">
              <h2>Connection telemetry</h2>
              {[
                ["Attempts", s.connectAttempts],
                ["Connected", s.connected],
                ["Connecting", s.connecting],
                ["Errors", s.connectErrors],
              ].map(([k, v]) => (
                <div className="plan-row" key={k}>
                  <span>{k}</span>
                  <strong className="mono">{number(Number(v))}</strong>
                </div>
              ))}
            </section>
            <section className="panel side-panel">
              <h2>Delivery telemetry</h2>
              {[
                ["Publish attempts", number(s.publishAttempts)],
                ["Publish errors", number(s.publishErrors)],
                ["Bytes sent", bytes(s.bytesSent)],
                ["Bytes received", bytes(s.bytesReceived)],
                ["P50 latency", latency(s.p50)],
              ].map(([k, v]) => (
                <div className="plan-row" key={k}>
                  <span>{k}</span>
                  <strong className="mono">{v}</strong>
                </div>
              ))}
            </section>
          </div>
        </>
      )}
    </>
  );
}
function readStorage<T>(key: string, fallback: T): T {
  try {
    return JSON.parse(localStorage.getItem(key) || "null") || fallback;
  } catch {
    return fallback;
  }
}
interface BrokerObservation {
  configured: boolean;
  status: string;
  error?: string;
  snapshot?: {
    timestamp: string;
    nodes: {
      name: string;
      status?: string;
      liveConnections?: number;
      cpuUse?: number;
      memoryUsed?: string;
    }[];
    metrics: Record<string, number>;
    connections?: number;
    messagesReceived?: number;
    messagesSent?: number;
    messagesDropped?: number;
  };
}
export function BrokerTelemetry() {
  const q = useQuery({
    queryKey: ["emqx"],
    queryFn: () => request<BrokerObservation>("/brokers/emqx"),
    refetchInterval: 10000,
    retry: false,
  });
  const data = q.data,
    s = data?.snapshot;
  const value = (n?: number) => (n === undefined ? "—" : number(n));
  return (
    <section className="panel">
      <div className="panel-heading">
        <div>
          <h2>
            <Radio size={17} /> EMQX broker observations
          </h2>
          <p>Broker-side counters from the management API</p>
        </div>
        {data && <Badge status={data.status} />}
      </div>
      {q.error ? (
        <ErrorNotice error={q.error} retry={() => q.refetch()} />
      ) : q.isLoading ? (
        <div className="loading">Loading broker observations…</div>
      ) : !data?.configured ? (
        <Empty
          title="EMQX monitoring is not configured"
          detail="Set the server's EMQX management API URL and API credentials to observe broker-side metrics."
        />
      ) : (
        <>
          <div className="metrics broker-metrics">
            <Metric
              label="BROKER CONNECTIONS"
              value={value(s?.connections)}
              accent
            />
            <Metric
              label="MESSAGES RECEIVED"
              value={value(s?.messagesReceived)}
            />
            <Metric label="MESSAGES SENT" value={value(s?.messagesSent)} />
            <Metric
              label="MESSAGES DROPPED"
              value={value(s?.messagesDropped)}
            />
          </div>
          {data.error && <ErrorNotice error={data.error} />}
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Node</th>
                  <th>Status</th>
                  <th>Connections</th>
                  <th>CPU use</th>
                  <th>Memory</th>
                </tr>
              </thead>
              <tbody>
                {s?.nodes?.map((n) => (
                  <tr key={n.name}>
                    <td className="mono">{n.name}</td>
                    <td>
                      <Badge status={n.status || "unknown"} />
                    </td>
                    <td className="mono">{value(n.liveConnections)}</td>
                    <td className="mono">
                      {n.cpuUse === undefined ? "—" : `${number(n.cpuUse)}%`}
                    </td>
                    <td className="mono">{n.memoryUsed || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="panel-footer">
            <span>Missing observations are shown as —</span>
            <span>
              {s?.timestamp
                ? `Updated ${new Date(s.timestamp).toLocaleTimeString()}`
                : "No snapshot available"}
            </span>
          </div>
        </>
      )}
    </section>
  );
}
export function Brokers() {
  const [profiles, setProfiles] = useState<
    { name: string; url: string; version: string }[]
  >(() => readStorage("mqtitan.brokers", []));
  const [name, setName] = useState("");
  const [url, setUrl] = useState("mqtt://localhost:1883");
  const [version, setVersion] = useState("3.1.1");
  const [error, setError] = useState("");
  function persist(next: typeof profiles) {
    setProfiles(next);
    localStorage.setItem("mqtitan.brokers", JSON.stringify(next));
  }
  return (
    <>
      <Title
        eyebrow="INFRASTRUCTURE / BROKERS"
        title="Broker profiles"
        detail="Reusable connection settings, stored locally in this browser."
      />
      <BrokerTelemetry />
      <div className="two-col">
        <section className="panel side-panel">
          <h2>Add a broker</h2>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              if (!name.trim()) {
                setError("Enter a profile name.");
                return;
              }
              if (!/^(mqtt|mqtts|ws|wss):\/\/.+/.test(url)) {
                setError("Enter a valid MQTT or WebSocket broker URL.");
                return;
              }
              persist([...profiles, { name: name.trim(), url, version }]);
              setName("");
              setError("");
            }}
          >
            <label>
              Profile name
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Production cluster"
              />
            </label>
            <label>
              Broker URL
              <input
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                required
              />
            </label>
            <label>
              MQTT version
              <select
                value={version}
                onChange={(e) => setVersion(e.target.value)}
              >
                <option>3.1.1</option>
                <option>5</option>
              </select>
            </label>
            {error && (
              <p role="alert" className="error-text">
                {error}
              </p>
            )}
            <button className="primary">
              <Plus size={15} /> Save profile
            </button>
          </form>
        </section>
        <section className="panel side-panel">
          <h2>
            Saved profiles <span className="count">{profiles.length}</span>
          </h2>
          {profiles.length ? (
            profiles.map((p, i) => (
              <div className="broker-item" key={i}>
                <Radio size={23} />
                <div>
                  <strong>{p.name}</strong>
                  <code>{p.url}</code>
                  <small>MQTT {p.version}</small>
                </div>
                <button
                  aria-label={`Remove ${p.name}`}
                  onClick={() => persist(profiles.filter((_, j) => j !== i))}
                >
                  <X size={15} />
                </button>
              </div>
            ))
          ) : (
            <Empty
              title="No broker profiles"
              detail="Save a connection to select it in the test wizard."
            />
          )}
        </section>
      </div>
    </>
  );
}
function Workers() {
  const workers = useQuery({
    queryKey: ["workers"],
    queryFn: () => request<Worker[]>("/workers"),
    refetchInterval: 5000,
  });
  const doctor = useQuery({
    queryKey: ["doctor"],
    queryFn: () => request<Check[]>("/doctor"),
  });
  return (
    <>
      <Title
        eyebrow="INFRASTRUCTURE / WORKERS"
        title="Load generators"
        detail="Worker capacity and host readiness, reported by the engine."
      >
        <button
          onClick={() => {
            workers.refetch();
            doctor.refetch();
          }}
        >
          <RefreshCw size={15} /> Refresh
        </button>
      </Title>
      {workers.error && <ErrorNotice error={workers.error} />}
      <div className="worker-grid">
        {workers.data?.map((w) => (
          <section className="panel side-panel" key={w.id}>
            <div className="worker-heading">
              <Server size={24} />
              <Badge status={w.status} />
            </div>
            <h2 className="mono">{w.id}</h2>
            <div className="plan-row">
              <span>CPU cores</span>
              <strong>{w.cpuCount}</strong>
            </div>
            <div className="plan-row">
              <span>Memory</span>
              <strong>{bytes(w.memoryBytes)}</strong>
            </div>
            <div className="plan-row">
              <span>Connected clients</span>
              <strong>{number(w.connected)}</strong>
            </div>
            <small className="subdued">
              Last seen {new Date(w.lastSeen).toLocaleString()}
            </small>
          </section>
        ))}
      </div>
      {!workers.isLoading && !workers.data?.length && !workers.error && (
        <section className="panel">
          <Empty
            title="No workers connected"
            detail="Start the server with a local worker or connect a remote generator."
          />
        </section>
      )}
      <section className="panel">
        <div className="panel-heading">
          <h2>
            <ShieldCheck size={17} /> Host readiness
          </h2>
        </div>
        {doctor.error ? (
          <ErrorNotice error={doctor.error} />
        ) : (
          <table>
            <thead>
              <tr>
                <th>Check</th>
                <th>Value</th>
                <th>Status</th>
                <th>Recommendation</th>
              </tr>
            </thead>
            <tbody>
              {doctor.data?.map((c) => (
                <tr key={c.Name}>
                  <td>{c.Name}</td>
                  <td className="mono">{c.Value}</td>
                  <td>
                    <Badge status={c.Status} />
                  </td>
                  <td className="subdued">{c.Recommendation || "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </>
  );
}
function Compare() {
  const q = useTests();
  const [a, setA] = useState("");
  const [b, setB] = useState("");
  const tests = q.data || [];
  const runs = [tests.find((t) => t.id === a), tests.find((t) => t.id === b)];
  return (
    <>
      <Title
        eyebrow="ANALYSIS / COMPARE"
        title="Put performance in perspective"
        detail="Compare two recorded runs using their actual result snapshots."
      />
      {q.error && <ErrorNotice error={q.error} />}
      <section className="panel side-panel">
        <div className="form-grid">
          {[a, b].map((value, i) => (
            <label key={i}>
              Run {i === 0 ? "A · baseline" : "B · candidate"}
              <select
                value={value}
                onChange={(e) =>
                  i === 0 ? setA(e.target.value) : setB(e.target.value)
                }
              >
                <option value="">Select a run</option>
                {tests.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name} · {new Date(t.startedAt).toLocaleString()}
                  </option>
                ))}
              </select>
            </label>
          ))}
        </div>
      </section>
      {runs.every(Boolean) ? (
        <section className="panel">
          <table>
            <thead>
              <tr>
                <th>Metric</th>
                {runs.map((t, i) => (
                  <th key={i}>{t!.name}</th>
                ))}
                <th>Change (B − A)</th>
              </tr>
            </thead>
            <tbody>
              {(
                [
                  "peakConnected",
                  "published",
                  "received",
                  "connectErrors",
                  "publishErrors",
                  "p50",
                  "p95",
                  "p99",
                  "bytesSent",
                ] as const
              ).map((k) => {
                const x = runs[0]!.snapshot[k],
                  y = runs[1]!.snapshot[k];
                const fmt = (n: number) =>
                  k.startsWith("p") && ["p50", "p95", "p99"].includes(k)
                    ? latency(n)
                    : k === "bytesSent"
                      ? bytes(n)
                      : number(n);
                return (
                  <tr key={k}>
                    <td>{k.replace(/([A-Z])/g, " $1")}</td>
                    <td className="mono">{fmt(x)}</td>
                    <td className="mono">{fmt(y)}</td>
                    <td className="mono">
                      {y > x ? "+" : ""}
                      {fmt(y - x)}{" "}
                      <span className="subdued">
                        {x
                          ? `(${(((y - x) / x) * 100).toFixed(1)}%)`
                          : "(no baseline)"}
                      </span>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </section>
      ) : (
        <section className="panel">
          <Empty
            title="Choose two runs to compare"
            detail="A baseline and a candidate reveal changes in throughput, latency, and reliability."
          />
        </section>
      )}
    </>
  );
}
function Errors() {
  const q = useTests();
  const navigate = useNavigate();
  const tests = (q.data || []).filter(
    (t) => t.error || t.snapshot.connectErrors || t.snapshot.publishErrors,
  );
  return (
    <>
      <Title
        eyebrow="OBSERVABILITY / ERRORS"
        title="Failure analysis"
        detail="Connection failures, delivery errors, and run-level diagnostics."
      />
      {q.error && <ErrorNotice error={q.error} />}
      <div className="metrics">
        <Metric label="AFFECTED RUNS" value={tests.length} />
        <Metric
          label="CONNECTION ERRORS"
          value={number(
            tests.reduce((n, t) => n + t.snapshot.connectErrors, 0),
          )}
        />
        <Metric
          label="PUBLISH ERRORS"
          value={number(
            tests.reduce((n, t) => n + t.snapshot.publishErrors, 0),
          )}
        />
        <Metric
          label="FAILED RUNS"
          value={tests.filter((t) => t.status === "failed").length}
        />
      </div>
      <section className="panel">
        <div className="panel-heading">
          <h2>Run diagnostics</h2>
        </div>
        {tests.length ? (
          tests.map((t) => (
            <button
              key={t.id}
              className="error-row"
              onClick={() =>
                navigate({ to: "/tests/$id", params: { id: t.id } })
              }
            >
              <TriangleAlert size={19} />
              <div>
                <strong>{t.name}</strong>
                <p>
                  {t.error ||
                    `${t.snapshot.connectErrors} connection errors · ${t.snapshot.publishErrors} publish errors`}
                </p>
              </div>
              <Badge status={t.status} />
              <ArrowUpRight size={16} />
            </button>
          ))
        ) : (
          <Empty
            title="No recorded errors"
            detail="Run diagnostics will appear here when the engine reports a failure."
          />
        )}
      </section>
    </>
  );
}
function SettingsPage() {
  const [token, setToken] = useState(
    sessionStorage.getItem("mqtitan.token") || "",
  );
  const [message, setMessage] = useState("");
  const qc = useQueryClient();
  return (
    <>
      <Title
        eyebrow="WORKSPACE / SETTINGS"
        title="Make the lab yours"
        detail="Browser preferences and API access for your workspace."
      />
      <section className="panel side-panel settings-panel">
        <h2>API authentication</h2>
        <p>
          If the server uses MQTITAN_TOKEN, enter its token here. It is kept for
          this browser session and sent with requests and live streams.
        </p>
        <label>
          Bearer token
          <input
            type="password"
            autoComplete="off"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder="Optional for local development"
          />
        </label>
        <button
          className="primary"
          onClick={() => {
            sessionStorage.setItem("mqtitan.token", token);
            qc.invalidateQueries();
            setMessage("API access settings saved.");
          }}
        >
          Save access settings
        </button>
        {message && (
          <p role="status" className="success">
            {message}
          </p>
        )}
        <div className="settings-rule" />
        <h2>Data & storage</h2>
        <p>
          Tests and metrics are stored by the server. Broker profiles and
          appearance preferences are stored in this browser.
        </p>
        <div className="plan-row">
          <span>API endpoint</span>
          <code>/api/v1</code>
        </div>
        <div className="plan-row">
          <span>Live transport</span>
          <strong>Server-sent events</strong>
        </div>
      </section>
    </>
  );
}
function Shell() {
  const navigate = useNavigate();
  const [wizard, setWizard] = useState(false);
  const [draft, setDraft] = useState<string>();
  const [theme, setTheme] = useState(
    () => localStorage.getItem("mqtitan.theme") || "dark",
  );
  const tests = useTests();
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("mqtitan.theme", theme);
  }, [theme]);
  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">
        Skip to content
      </a>
      <aside className="sidebar">
        <Link to="/" className="brand">
          <div className="brand-symbol">
            <Layers size={25} />
          </div>
          <span>
            MQ<span className="brand-accent">Titan</span>
            <small>PERFORMANCE LAB</small>
          </span>
        </Link>
        <div className="workspace">
          <div className="workspace-icon">M</div>
          <div>
            Local workspace<small>MQTT load testing</small>
          </div>
          <ChevronRight size={14} />
        </div>
        <div className="nav-label">WORKSPACE</div>
        <nav aria-label="Main navigation">
          {nav.map(([label, to, Icon]) => (
            <Link
              key={to}
              to={to}
              aria-label={label}
              activeProps={{ className: "active" }}
              activeOptions={{ exact: to === "/" }}
            >
              <Icon size={18} />
              <span>{label}</span>
              {label === "Tests" &&
                tests.data?.some((t) => t.status === "running") && (
                  <span className="nav-dot" />
                )}
            </Link>
          ))}
        </nav>
        <div className="sidebar-bottom">
          <div className="engine-state">
            <i className={tests.error ? "offline" : ""} />
            <span>
              {tests.error
                ? "Engine unavailable"
                : tests.isLoading
                  ? "Connecting to engine"
                  : "Engine connected"}
            </span>
            <small>v1alpha1</small>
          </div>
          <button
            className="theme-button"
            onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
          >
            <Sun size={16} />
            {theme === "dark" ? "Light appearance" : "Dark appearance"}
          </button>
        </div>
      </aside>
      <div className="main-column">
        <header className="topbar">
          <div className="breadcrumb">
            <span>Workspace</span>
            <ChevronRight size={13} />
            <strong>MQTT performance lab</strong>
          </div>
          <div className="topbar-actions">
            <span className="environment">
              <i /> LOCAL
            </span>
            <button
              aria-label="Create a new test"
              className="topbar-new"
              onClick={() => setWizard(true)}
            >
              <Plus size={15} /> New test
            </button>
          </div>
        </header>
        <main id="main-content" tabIndex={-1}>
          <PageContent
            tests={tests.data || []}
            error={tests.error}
            loading={tests.isLoading}
            retry={() => tests.refetch()}
            create={() => setWizard(true)}
            draft={draft}
            clearDraft={() => setDraft(undefined)}
          />
        </main>
        <footer>
          <span>
            MQTitan <span className="subdued">/ Engineered for scale.</span>
          </span>
          <span className="mono">MQTT 3.1.1 · MQTT 5.0</span>
        </footer>
      </div>
      {wizard && (
        <Wizard
          close={() => setWizard(false)}
          done={(yaml) => {
            setDraft(yaml);
            setWizard(false);
            navigate({ to: "/scenarios" });
          }}
        />
      )}
    </div>
  );
}
function PageContent({
  tests,
  error,
  loading,
  retry,
  create,
  draft,
}: {
  tests: Test[];
  error: unknown;
  loading: boolean;
  retry: () => void;
  create: () => void;
  draft?: string;
  clearDraft: () => void;
}) {
  const navigate = useNavigate();
  const path = useRouterState({ select: (state) => state.location.pathname });
  const open = (id: string) => navigate({ to: "/tests/$id", params: { id } });
  if (path.startsWith("/tests/") && path.split("/")[2])
    return <TestDetail id={decodeURIComponent(path.split("/")[2])} />;
  if (path === "/scenarios")
    return <ScenarioEditor initial={draft} onStarted={open} />;
  if (path === "/brokers") return <Brokers />;
  if (path === "/workers") return <Workers />;
  if (path === "/compare") return <Compare />;
  if (path === "/errors") return <Errors />;
  if (path === "/settings") return <SettingsPage />;
  return (
    <>
      {error && <ErrorNotice error={error} retry={retry} />}{" "}
      {loading ? (
        <div className="loading">
          <RefreshCw className="spin" size={20} /> Connecting to engine…
        </div>
      ) : path === "/tests" ? (
        <>
          <Title
            eyebrow="WORKLOADS / TESTS"
            title="Every run. Every result."
            detail="Live tests and historical benchmarks in one place."
          >
            <button className="primary" onClick={create}>
              <Plus size={16} /> New test
            </button>
          </Title>
          <RunTable tests={tests} open={open} />
        </>
      ) : (
        <Dashboard tests={tests} open={open} create={create} />
      )}
    </>
  );
}
const rootRoute = createRootRoute({ component: Shell });
const routes = nav.map(([, path]) =>
  createRoute({ getParentRoute: () => rootRoute, path, component: () => null }),
);
const detailRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/tests/$id",
  component: () => null,
});
export const router = createRouter({
  routeTree: rootRoute.addChildren([...routes, detailRoute]),
  defaultPreload: "intent",
});
declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
export default function App() {
  return <RouterProvider router={router} />;
}
