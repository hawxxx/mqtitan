import { useEffect, useRef, useState } from "react";
import {
  ArrowUpRight,
  Radio,
  RotateCcw,
  SlidersHorizontal,
  Zap,
} from "lucide-react";
import { LoadSlider } from "./LoadSlider";
import { number, request, type Test } from "./model";

export function LiveLoadControls({
  test,
  onUpdated,
}: {
  test: Test;
  onUpdated: (test: Test) => void;
}) {
  const control = test.loadControl!;
  const target = control.manual ? control.clients : test.snapshot.targetClients;
  const [clients, setClients] = useState(target);
  const [rate, setRate] = useState(control.ratePerClient);
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const alive = useRef(true);
  const controller = useRef<AbortController | null>(null);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      controller.current?.abort();
    };
  }, []);
  useEffect(() => {
    if (!dirty && !busy) {
      setClients(target);
      setRate(control.ratePerClient);
    }
  }, [target, control.ratePerClient, dirty, busy]);
  const valid =
    Number.isInteger(clients) &&
    clients >= 0 &&
    clients <= control.capacityClients &&
    Number.isFinite(rate) &&
    rate >= 0 &&
    rate <= control.maxRatePerClient;
  const running = test.status === "running";
  function resetDraft() {
    setClients(target);
    setRate(control.ratePerClient);
    setDirty(false);
    setError("");
    setMessage("");
  }
  function editClients(value: number) {
    setClients(value);
    setDirty(true);
    setMessage("");
  }
  function editRate(value: number) {
    setRate(value);
    setDirty(true);
    setMessage("");
  }
  async function apply() {
    if (!valid || !running || busy) return;
    setBusy(true);
    setError("");
    setMessage("");
    const abort = new AbortController();
    controller.current = abort;
    try {
      const updated = await request<Test>(`/tests/${test.id}/load`, {
        method: "POST",
        signal: abort.signal,
        body: JSON.stringify({
          clients,
          ratePerClient: rate,
          revision: control.revision,
        }),
      });
      if (!alive.current) return;
      onUpdated(updated);
      setDirty(false);
      setMessage(
        `Load request #${updated.loadControl?.revision} accepted. Connections ramp toward the new target.`,
      );
    } catch (err) {
      if (alive.current && !abort.signal.aborted)
        setError((err as Error).message);
    } finally {
      if (alive.current) setBusy(false);
    }
  }
  return (
    <section className="panel live-load" aria-labelledby="live-load-title">
      <div className="panel-heading">
        <div>
          <h2 id="live-load-title">
            <SlidersHorizontal size={16} aria-hidden="true" /> Live load
            controls
          </h2>
          <p>
            {control.manual
              ? "Manual override active"
              : "Following scenario stages"}{" "}
            · capacity {number(control.capacityClients)} clients
          </p>
        </div>
        <div className="live-load-actions">
          <span className={`draft-indicator ${dirty ? "is-dirty" : ""}`}>
            <i />
            {dirty ? "Unapplied changes" : "No draft changes"}
          </span>
          {dirty && (
            <button
              className="load-reset"
              type="button"
              disabled={busy || !running}
              onClick={resetDraft}
            >
              <RotateCcw size={14} aria-hidden="true" /> Reset draft
            </button>
          )}
          <button
            className="primary"
            type="button"
            disabled={!running || !dirty || !valid || busy}
            onClick={apply}
          >
            {busy ? "Applying…" : "Apply load"}
            <ArrowUpRight size={15} aria-hidden="true" />
          </button>
        </div>
      </div>
      <fieldset className="live-load-fields" disabled={!running || busy}>
        <div className="live-load-field">
          <label htmlFor={`clients-${test.id}`}>
            <Radio size={15} aria-hidden="true" /> Client target
          </label>
          <input
            id={`clients-${test.id}`}
            name="clientTarget"
            autoComplete="off"
            aria-describedby={`clients-hint-${test.id}`}
            type="number"
            min={0}
            max={control.capacityClients}
            step={1}
            value={clients}
            aria-invalid={
              !Number.isInteger(clients) ||
              clients < 0 ||
              clients > control.capacityClients
            }
            onChange={(event) => editClients(Number(event.target.value))}
          />
          <LoadSlider
            label="Client target slider"
            max={control.capacityClients}
            value={clients}
            unit="clients"
            onChange={editClients}
          />
          <div className="load-presets" aria-label="Client target presets">
            {[0, 25, 50, 100].map((percent) => (
              <button
                key={percent}
                type="button"
                aria-label={`Set clients to ${percent}% capacity`}
                aria-pressed={
                  clients ===
                  Math.round((control.capacityClients * percent) / 100)
                }
                onClick={() =>
                  editClients(
                    Math.round((control.capacityClients * percent) / 100),
                  )
                }
              >
                {percent === 0 ? "Disconnect" : `${percent}%`}
              </button>
            ))}
          </div>
          <span id={`clients-hint-${test.id}`} className="load-field-hint">
            0 disconnects all clients · {number(test.snapshot.connected)}{" "}
            currently connected
          </span>
        </div>
        <div className="live-load-field">
          <label htmlFor={`rate-${test.id}`}>
            <Zap size={15} aria-hidden="true" /> Messages / publisher / sec
          </label>
          <input
            id={`rate-${test.id}`}
            name="publishRate"
            autoComplete="off"
            aria-describedby={`rate-hint-${test.id}`}
            type="number"
            min={0}
            max={control.maxRatePerClient}
            step="any"
            value={rate}
            aria-invalid={
              !Number.isFinite(rate) ||
              rate < 0 ||
              rate > control.maxRatePerClient
            }
            disabled={!control.hasPublishers}
            onChange={(event) => editRate(Number(event.target.value))}
          />
          <LoadSlider
            label="Message rate slider"
            value={rate}
            max={control.maxRatePerClient}
            unit="msg/s"
            logarithmic
            disabled={!control.hasPublishers}
            onChange={editRate}
          />
          <div className="load-presets" aria-label="Message rate presets">
            {[0, 1, 10, 100]
              .filter((value) => value <= control.maxRatePerClient)
              .map((value) => (
                <button
                  key={value}
                  type="button"
                  disabled={!control.hasPublishers}
                  aria-label={
                    value === 0
                      ? "Pause publishing"
                      : `Set rate to ${value} messages per publisher per second`
                  }
                  aria-pressed={rate === value}
                  onClick={() => editRate(value)}
                >
                  {value === 0 ? "Pause" : `${value} msg/s`}
                </button>
              ))}
          </div>
          <span id={`rate-hint-${test.id}`} className="load-field-hint">
            {control.hasPublishers
              ? "0 pauses publishing · applies to every publisher group"
              : "No publishers in this scenario"}
          </span>
        </div>
      </fieldset>
      <div className="live-load-note">
        {running
          ? "Move sliders, then Apply. Changes override client stages but do not extend test duration. Rate slider is logarithmic; use numbers for exact values."
          : "Run ended. Controls are read-only."}
      </div>
      {!!control.pendingWorkers?.length && (
        <p className="live-load-note" role="status">
          Waiting for worker acknowledgement:{" "}
          {control.pendingWorkers.join(", ")}
        </p>
      )}
      {!valid && (
        <p className="notice error" role="alert">
          Choose 0–{control.capacityClients} clients and 0–
          {control.maxRatePerClient} messages/sec.
        </p>
      )}
      {error && (
        <p className="notice error" role="alert">
          {error}
        </p>
      )}
      {message && (
        <p className="live-load-note" role="status">
          {message}
        </p>
      )}
      {!!test.loadChanges?.length && (
        <div className="load-change-history">
          <h3>Load change history</h3>
          <ol>
            {test.loadChanges
              .slice(-8)
              .reverse()
              .map((change) => (
                <li key={change.revision}>
                  <time>{new Date(change.timestamp).toLocaleTimeString()}</time>
                  <span>#{change.revision}</span>
                  <strong>{number(change.clients)} clients</strong>
                  <span>{number(change.ratePerClient)} msg/publisher/sec</span>
                </li>
              ))}
          </ol>
          <p className="subdued">
            Latest 8 requests shown. Complete history is included in JSON
            reports.
          </p>
        </div>
      )}
    </section>
  );
}
