import { useSyncExternalStore } from "react";

// The display time zone is a per-browser preference: "local", "UTC" or an
// IANA name. Every timestamp formatter reads it through useTimeZone.
const key = "mqtitan.timezone";
const listeners = new Set<() => void>();

function read(): string {
  try {
    const v = localStorage.getItem(key);
    if (v && (v === "local" || validZone(v))) return v;
  } catch {
    /* Preference is optional. */
  }
  return "local";
}
let current = read();

export function validZone(zone: string) {
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: zone });
    return true;
  } catch {
    return false;
  }
}

export function setTimeZone(zone: string) {
  current = zone;
  try {
    localStorage.setItem(key, zone);
  } catch {
    /* Preference is optional. */
  }
  listeners.forEach((l) => l());
}

export function useTimeZone() {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => current,
  );
}

export const localZone = () => Intl.DateTimeFormat().resolvedOptions().timeZone;
const resolve = (zone: string) => (zone === "local" ? undefined : zone);

// Short zone label such as "UTC", "GMT-5" or "EST" for a moment.
export function zoneLabel(zone: string, at = Date.now()) {
  const part = new Intl.DateTimeFormat("en-US", {
    timeZone: resolve(zone),
    timeZoneName: "short",
  })
    .formatToParts(at)
    .find((p) => p.type === "timeZoneName");
  return part?.value || zone;
}

// 24-hour clock with milliseconds, for logs.
export function clock(ms: number, zone: string) {
  const parts = new Intl.DateTimeFormat("en-GB", {
    timeZone: resolve(zone),
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  }).format(ms);
  return `${parts}.${String(Math.floor(((ms % 1000) + 1000) % 1000)).padStart(3, "0")}`;
}

export function dateTime(ms: number, zone: string) {
  return `${new Intl.DateTimeFormat("en-CA", {
    timeZone: resolve(zone),
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(ms)} ${clock(ms, zone).slice(0, 8)} ${zoneLabel(zone, ms)}`;
}

export function day(ms: number, zone: string) {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: resolve(zone),
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(ms);
}

export const commonZones = [
  "America/Los_Angeles",
  "America/Denver",
  "America/Chicago",
  "America/New_York",
  "America/Bogota",
  "America/Sao_Paulo",
  "Europe/London",
  "Europe/Berlin",
  "Africa/Johannesburg",
  "Asia/Dubai",
  "Asia/Kolkata",
  "Asia/Singapore",
  "Asia/Tokyo",
  "Australia/Sydney",
];

export function allZones(): string[] {
  const intl = Intl as typeof Intl & {
    supportedValuesOf?: (key: string) => string[];
  };
  try {
    return intl.supportedValuesOf?.("timeZone") || commonZones;
  } catch {
    return commonZones;
  }
}
