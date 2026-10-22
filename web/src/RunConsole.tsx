import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { ChevronDown, Pause, Play, SquareTerminal } from "lucide-react";
import type { LogEvent, Sample, Snapshot } from "./model";
import { elapsed, latency, number } from "./model";
import { clock, dateTime, day, useTimeZone, zoneLabel } from "./timezone";

export type ConsoleLevel =
  "debug" | "info" | "success" | "action" | "warn" | "error";
export type ConsoleLine = {
  key: string;
  time: number;
  level: ConsoleLevel;
  message: string;
};

const levels: ConsoleLevel[] = [
  "success",
  "info",
  "action",
  "warn",
  "error",
  "debug",
];
const labels: Record<ConsoleLevel, string> = {
  debug: "STAT",
  info: "INFO",
  success: "DONE",
  action: "USER",
  warn: "WARN",
  error: "FAIL",
};
const WINDOW_MS = 5000;
const SUMMARY_MS = 15000;
const MAX_LINES = 2000;

function reasons(
  current: Record<string, number> | undefined,
  previous: Record<string, number> | undefined,
) {
  return Object.entries(current || {})
    .map(([k, v]) => [k, v - (previous?.[k] || 0)] as const)
    .filter(([, d]) => d > 0)
    .map(([k, d]) => [k.replace(/_/g, " "), d] as const);
}

// deriveConsoleLines turns consecutive metric samples into readable events.
// Noisy counters are batched into five-second windows so a struggling run
// produces a handful of lines, not one per second.
export function deriveConsoleLines(
  samples: Sample[],
  startedAt: string,
): ConsoleLine[] {
  const out: ConsoleLine[] = [];
  const start = Date.parse(startedAt);
  let prev: Snapshot | undefined;
  let milestone = 0;
  let target = 0;
  let reached = false;
  let lastSummary = -Infinity;
  const pending = new Map<
    string,
    { since: number; total: number; detail: Map<string, number> }
  >();
  const add = (time: number, level: ConsoleLevel, message: string) =>
    out.push({ key: `d${out.length}`, time, level, message });
  const accumulate = (
    kind: string,
    time: number,
    delta: number,
    detail: (readonly [string, number])[] = [],
  ) => {
    if (delta <= 0) return;
    const p = pending.get(kind) || { since: time, total: 0, detail: new Map() };
    p.total += delta;
    for (const [name, count] of detail)
      p.detail.set(name, (p.detail.get(name) || 0) + count);
    pending.set(kind, p);
  };
  const flush = (time: number, force = false) => {
    for (const [kind, p] of pending) {
      if (!force && time - p.since < WINDOW_MS) continue;
      const detail = [...p.detail]
        .sort((a, b) => b[1] - a[1])
        .map(([k, v]) => `${k} ×${number(v)}`)
        .join(", ");
      const [level, text] = (
        {
          connect: ["error", "connection errors"],
          publish: ["error", "publish errors"],
          subscribe: ["error", "subscribe errors"],
          slots: [
            "warn",
            "missed publish slots (generator behind the requested rate)",
          ],
          reconnect: ["warn", "reconnects"],
          disconnect: ["warn", "disconnects"],
        } as Record<string, [ConsoleLevel, string]>
      )[kind];
      add(
        time,
        level,
        `+${number(p.total)} ${text}${detail ? `: ${detail}` : ""}`,
      );
      pending.delete(kind);
    }
  };
  for (const sample of samples) {
    const s = sample.snapshot;
    const time = Date.parse(sample.timestamp);
    if (s.targetClients !== target) {
      target = s.targetClients;
      milestone = 0;
      reached = false;
    }
    if (target > 0) {
      const quarter = Math.min(3, Math.floor((s.connected / target) * 4));
      if (quarter > milestone) {
        milestone = quarter;
        add(
          time,
          "info",
          `Connected ${number(s.connected)} / ${number(target)} (${quarter * 25}%)`,
        );
      }
      if (!reached && s.connected >= target) {
        reached = true;
        milestone = 3;
        add(
          time,
          "success",
          `All ${number(target)} target clients connected after ${elapsed((time - start) / 1000)}`,
        );
      }
    }
    if (prev) {
      const drop = prev.connected - s.connected;
      if (
        drop > Math.max(10, prev.connected * 0.01) &&
        s.targetClients >= prev.targetClients
      )
        add(
          time,
          "warn",
          `Connected dropped ${number(prev.connected)} → ${number(s.connected)}`,
        );
      if (s.connected < target) reached = false;
      accumulate("connect", time, s.connectErrors - prev.connectErrors, [
        ...reasons(s.connectFailures, prev.connectFailures),
        ...reasons(s.connackReasons, prev.connackReasons),
      ]);
      accumulate("publish", time, s.publishErrors - prev.publishErrors);
      accumulate(
        "subscribe",
        time,
        (s.subscribeErrors || 0) - (prev.subscribeErrors || 0),
      );
      accumulate(
        "slots",
        time,
        (s.publishSlotsMissed || 0) - (prev.publishSlotsMissed || 0),
      );
      accumulate(
        "reconnect",
        time,
        (s.reconnects || 0) - (prev.reconnects || 0),
      );
      accumulate(
        "disconnect",
        time,
        (s.disconnects || 0) - (prev.disconnects || 0),
      );
      flush(time);
      if (time - lastSummary >= SUMMARY_MS) {
        lastSummary = time;
        add(
          time,
          "debug",
          `${number(s.connected)} connected · ${number(Math.round(sample.messageRate))} msg/s · p95 ${latency(s.p95)}`,
        );
      }
    }
    prev = s;
  }
  if (samples.length)
    flush(Date.parse(samples[samples.length - 1].timestamp), true);
  return out;
}

const storageKey = "mqtitan.console.open";
function readOpen() {
  try {
    return localStorage.getItem(storageKey) !== "false";
  } catch {
    return true;
  }
}

export function RunConsole({
  startedAt,
  samples,
  events,
  running,
  tall = false,
}: {
  startedAt: string;
  samples: Sample[];
  events: LogEvent[];
  running: boolean;
  tall?: boolean;
}) {
  const zone = useTimeZone();
  const [stored, setOpen] = useState(readOpen);
  const open = tall || stored;
  const [follow, setFollow] = useState(true);
  const [hidden, setHidden] = useState<Set<ConsoleLevel>>(() => new Set());
  const body = useRef<HTMLOListElement>(null);
  const start = Date.parse(startedAt);
  const lines = useMemo(() => {
    const server: ConsoleLine[] = events.map((e) => ({
      key: `s${e.seq}`,
      time: Date.parse(e.time),
      level:
        e.level === "info" &&
        /^(Test completed|Threshold \S+ passed)/.test(e.message)
          ? "success"
          : e.level,
      message: e.message,
    }));
    return [...server, ...deriveConsoleLines(samples, startedAt)]
      .sort((a, b) => a.time - b.time)
      .slice(-MAX_LINES);
  }, [events, samples, startedAt]);
  const counts = useMemo(() => {
    const c = Object.fromEntries(levels.map((l) => [l, 0])) as Record<
      ConsoleLevel,
      number
    >;
    for (const l of lines) c[l.level]++;
    return c;
  }, [lines]);
  const visible = lines.filter((l) => !hidden.has(l.level));
  useEffect(() => {
    if (open && follow && body.current)
      body.current.scrollTop = body.current.scrollHeight;
  }, [open, follow, visible.length]);
  const toggle = () => {
    if (tall) return;
    setOpen(!stored);
    try {
      localStorage.setItem(storageKey, String(!stored));
    } catch {
      /* Preference is optional. */
    }
  };
  return (
    <section
      className={`panel run-console${open ? " open" : ""}${tall ? " tall" : ""}`}
    >
      <div className="run-console-bar">
        <button
          className="run-console-toggle"
          aria-expanded={open}
          aria-controls="run-console-body"
          onClick={toggle}
        >
          <SquareTerminal size={16} />
          <span>Run log</span>
          {running && <i className="run-console-live" aria-label="live" />}
          <ChevronDown size={15} className="run-console-chevron" />
        </button>
        <div
          className="run-console-filters"
          role="group"
          aria-label="Filter levels"
        >
          {levels
            .filter((l) => counts[l] > 0)
            .map((l) => (
              <button
                key={l}
                className={`console-chip level-${l}`}
                aria-pressed={!hidden.has(l)}
                onClick={() => {
                  const next = new Set(hidden);
                  if (!next.delete(l)) next.add(l);
                  setHidden(next);
                }}
              >
                {labels[l]} {number(counts[l])}
              </button>
            ))}
          {open && running && (
            <button
              className="console-chip"
              aria-pressed={follow}
              onClick={() => setFollow(!follow)}
              title={follow ? "Stop following new lines" : "Follow new lines"}
            >
              {follow ? <Pause size={12} /> : <Play size={12} />}
              {follow ? "Following" : "Paused"}
            </button>
          )}
        </div>
      </div>
      {open && (
        <ol
          id="run-console-body"
          ref={body}
          className="run-console-body"
          role="log"
          aria-label="Run log"
          onScroll={(e) => {
            const el = e.currentTarget;
            const atEnd = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
            if (atEnd !== follow && running) setFollow(atEnd);
          }}
        >
          {visible.length ? (
            visible.map((l, i) => {
              const today = day(l.time, zone);
              const newDay =
                i === 0 || today !== day(visible[i - 1].time, zone);
              return (
                <Fragment key={l.key}>
                  {newDay && (
                    <li className="console-day" aria-hidden="true">
                      {today} · {zoneLabel(zone, l.time)}
                    </li>
                  )}
                  <li className={`level-${l.level}`}>
                    <time
                      dateTime={new Date(l.time).toISOString()}
                      title={dateTime(l.time, zone)}
                    >
                      {clock(l.time, zone)}
                    </time>
                    <span className="console-elapsed">
                      +{elapsed(Math.max(0, l.time - start) / 1000)}
                    </span>
                    <span className="console-level">{labels[l.level]}</span>
                    <span className="console-message">{l.message}</span>
                  </li>
                </Fragment>
              );
            })
          ) : (
            <li className="level-debug">
              <span className="console-message">
                {lines.length
                  ? "All levels are filtered out."
                  : "Waiting for the first events…"}
              </span>
            </li>
          )}
        </ol>
      )}
    </section>
  );
}
