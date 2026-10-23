import { useEffect, useMemo, useRef, useState } from "react";
import { Check, ChevronDown, Globe } from "lucide-react";
import {
  allZones,
  clock,
  commonZones,
  localZone,
  setTimeZone,
  useTimeZone,
  zoneLabel,
} from "./timezone";

function useNow(every = 1000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), every);
    return () => clearInterval(t);
  }, [every]);
  return now;
}

export function TimeZonePicker() {
  const zone = useTimeZone();
  const now = useNow();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const root = useRef<HTMLDivElement>(null);
  const search = useRef<HTMLInputElement>(null);
  const zones = useMemo(() => allZones(), []);
  // Zone abbreviations are computed once per opening, not every clock tick.
  const labels = useMemo(
    () =>
      open
        ? new Map(
            ["local", "UTC", ...zones].map((z) => [z, zoneLabel(z)] as const),
          )
        : new Map<string, string>(),
    [open, zones],
  );
  useEffect(() => {
    if (!open) return;
    search.current?.focus();
    const close = (e: MouseEvent | KeyboardEvent) => {
      if (
        e instanceof KeyboardEvent
          ? e.key === "Escape"
          : !root.current?.contains(e.target as Node)
      )
        setOpen(false);
    };
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", close);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", close);
    };
  }, [open]);
  const q = query.trim().toLowerCase().replace(/\s+/g, "_");
  const match = (z: string) => !q || z.toLowerCase().includes(q);
  const pick = (z: string) => {
    setTimeZone(z);
    setOpen(false);
    setQuery("");
  };
  const option = (z: string, label = z.replace(/_/g, " ")) => (
    <li key={z}>
      <button
        role="option"
        aria-selected={zone === z}
        className={zone === z ? "selected" : ""}
        onClick={() => pick(z)}
      >
        <span>{label}</span>
        <small>{labels.get(z)}</small>
        {zone === z && <Check size={13} />}
      </button>
    </li>
  );
  const others = zones.filter(
    (z) => !commonZones.includes(z) && z !== "UTC" && match(z),
  );
  return (
    <div className="tz-picker" ref={root}>
      <button
        className="tz-trigger"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={`Display time zone: ${zone === "local" ? `local (${localZone()})` : zone}`}
        onClick={() => setOpen(!open)}
      >
        <Globe size={14} />
        <span className="tz-clock mono">{clock(now, zone).slice(0, 8)}</span>
        <span className="tz-name">{zoneLabel(zone, now)}</span>
        <ChevronDown size={13} />
      </button>
      {open && (
        <div
          className="tz-menu"
          role="dialog"
          aria-label="Choose display time zone"
        >
          <input
            ref={search}
            type="search"
            placeholder="Search time zones"
            aria-label="Search time zones"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          <ul role="listbox" aria-label="Time zones">
            {match("local") &&
              option("local", `Local · ${localZone().replace(/_/g, " ")}`)}
            {match("utc") && option("UTC", "UTC")}
            {commonZones.some(match) && <li className="tz-group">Common</li>}
            {commonZones.filter(match).map((z) => option(z))}
            {others.length > 0 && <li className="tz-group">All zones</li>}
            {others.map((z) => option(z))}
          </ul>
        </div>
      )}
    </div>
  );
}
