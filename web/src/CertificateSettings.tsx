import { useEffect, useRef, useState } from "react";
import { ShieldCheck, Upload } from "lucide-react";
import { request } from "./model";

export interface TLSSelection {
  profile?: string;
  serverName?: string;
}
interface CertificateProfile {
  id: string;
  name: string;
  hasCa: boolean;
  hasClientCertificate: boolean;
  expiresAt: string;
  fingerprint?: string;
}

const maxFileBytes = 128 * 1024;
type Files = { ca?: File; cert?: File; key?: File };

function readFile(file?: File): Promise<string> {
  if (!file) return Promise.resolve("");
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () =>
      reject(new Error("Could not read certificate file."));
    reader.onload = () => resolve(String(reader.result || ""));
    reader.readAsText(file);
  });
}

export function CertificateSettings({
  value,
  onChange,
  onBusyChange,
}: {
  value: TLSSelection;
  onChange: (value: TLSSelection) => void;
  onBusyChange?: (busy: boolean) => void;
}) {
  const [profiles, setProfiles] = useState<CertificateProfile[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [uploadOpen, setUploadOpen] = useState(false);
  const [files, setFiles] = useState<Files>({});
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [fileGeneration, setFileGeneration] = useState(0);
  const active = useRef(true);
  const uploadController = useRef<AbortController | null>(null);
  useEffect(() => {
    onBusyChange?.(busy || Boolean(files.ca || files.cert || files.key));
  }, [busy, files, onBusyChange]);
  useEffect(() => {
    active.current = true;
    const controller = new AbortController();
    request<CertificateProfile[]>("/certificates", {
      signal: controller.signal,
    })
      .then((data) => {
        if (active.current) setProfiles(Array.isArray(data) ? data : []);
      })
      .catch((err) => {
        if (active.current && !controller.signal.aborted)
          setLoadError((err as Error).message);
      })
      .finally(() => {
        if (active.current) setLoading(false);
      });
    return () => {
      active.current = false;
      controller.abort();
      uploadController.current?.abort();
    };
  }, []);

  function chooseFile(kind: keyof Files, file?: File) {
    setError("");
    setMessage("");
    if (file && file.size > maxFileBytes) {
      setError("Each PEM file must be at most 128 KiB.");
      setFiles((previous) => ({ ...previous, [kind]: undefined }));
      return;
    }
    setFiles((previous) => ({ ...previous, [kind]: file }));
  }

  async function upload() {
    setError("");
    setMessage("");
    if (!name.trim()) {
      setError("Enter a certificate profile name.");
      return;
    }
    if (Boolean(files.cert) !== Boolean(files.key)) {
      setError("Upload the client certificate and private key together.");
      return;
    }
    if (!files.ca && !files.cert) {
      setError("Choose a CA bundle or client certificate and private key.");
      return;
    }
    if (
      window.location.protocol !== "https:" &&
      !["localhost", "127.0.0.1", "[::1]"].includes(window.location.hostname)
    ) {
      setError(
        "Use HTTPS for remote certificate uploads. Local loopback HTTP is allowed.",
      );
      return;
    }
    const controller = new AbortController();
    uploadController.current = controller;
    setBusy(true);
    try {
      const [caPem, certPem, keyPem] = await Promise.all([
        readFile(files.ca),
        readFile(files.cert),
        readFile(files.key),
      ]);
      if (!active.current) return;
      const profile = await request<CertificateProfile>("/certificates", {
        method: "POST",
        signal: controller.signal,
        body: JSON.stringify({
          name: name.trim(),
          ...(caPem ? { caPem } : {}),
          ...(certPem ? { certPem, keyPem } : {}),
        }),
      });
      if (!active.current) return;
      setProfiles((previous) => [profile, ...previous]);
      setLoadError("");
      onChange({ ...value, profile: profile.id });
      setFiles({});
      setFileGeneration((previous) => previous + 1);
      setName("");
      setMessage(
        profile.hasClientCertificate
          ? "Certificate profile saved. Private key is encrypted and cannot be downloaded."
          : "Certificate profile saved. CA trust is ready to use.",
      );
    } catch (err) {
      if (active.current && !controller.signal.aborted)
        setError((err as Error).message);
    } finally {
      if (active.current) setBusy(false);
    }
  }
  const selected = profiles.find((profile) => profile.id === value.profile);
  return (
    <fieldset className="tls-settings" disabled={busy}>
      <legend>
        <ShieldCheck size={15} /> TLS & certificates
      </legend>
      <p className="subdued">
        For mqtts:// and wss://. Publicly trusted endpoints need no certificate
        upload.
      </p>
      <div className="form-grid">
        <label>
          TLS certificate profile
          <select
            value={value.profile || ""}
            onChange={(event) =>
              onChange({ ...value, profile: event.target.value || undefined })
            }
          >
            <option value="">System trust · no client certificate</option>
            {value.profile && !selected && (
              <option value={value.profile}>
                Selected profile · {value.profile}
              </option>
            )}
            {profiles.map((profile) => (
              <option key={profile.id} value={profile.id}>
                {profile.name} ·{" "}
                {profile.hasClientCertificate ? "mTLS" : "custom CA"}
              </option>
            ))}
          </select>
        </label>
        <label>
          Server name / SNI (optional)
          <input
            autoComplete="off"
            placeholder="Defaults to broker hostname"
            value={value.serverName || ""}
            onChange={(event) =>
              onChange({
                ...value,
                serverName: event.target.value || undefined,
              })
            }
          />
        </label>
      </div>
      {loading && (
        <p className="subdued" role="status">
          Loading certificate profiles…
        </p>
      )}
      {loadError && (
        <p className="notice error" role="alert">
          Certificate profiles unavailable: {loadError}
        </p>
      )}
      {selected && (
        <div className="tls-profile-details">
          <span>
            {selected.hasCa ? "Custom CA trust" : "System CA trust"} ·{" "}
            {selected.hasClientCertificate
              ? "Client certificate enabled"
              : "No client certificate"}
          </span>
          <span>
            Expires {new Date(selected.expiresAt).toLocaleDateString()}
          </span>
          {selected.fingerprint && (
            <code title="SHA-256 fingerprint">
              SHA-256 {selected.fingerprint.slice(0, 16)}…
            </code>
          )}
        </div>
      )}
      <button
        type="button"
        className="text-button"
        onClick={() => setUploadOpen((previous) => !previous)}
        aria-expanded={uploadOpen}
      >
        <Upload size={14} />{" "}
        {uploadOpen ? "Hide certificate upload" : "Upload certificates"}
      </button>
      {uploadOpen && (
        <div className="tls-upload">
          <label>
            Certificate profile name
            <input
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={80}
              autoComplete="off"
              placeholder="Production MQTT client"
            />
          </label>
          <div className="form-grid" key={fileGeneration}>
            <label>
              CA bundle (optional)
              <input
                type="file"
                accept=".pem,.crt,.cer"
                onChange={(event) => chooseFile("ca", event.target.files?.[0])}
              />
            </label>
            <label>
              Client certificate (optional)
              <input
                type="file"
                accept=".pem,.crt,.cer"
                onChange={(event) =>
                  chooseFile("cert", event.target.files?.[0])
                }
              />
            </label>
            <label>
              Client private key (optional)
              <input
                type="file"
                accept=".pem,.key"
                onChange={(event) => chooseFile("key", event.target.files?.[0])}
              />
            </label>
          </div>
          <p className="subdued">
            PEM format, 128 KiB per file. Unencrypted private keys only. Files
            are sent to the controller, stored encrypted, and never saved in
            browser storage. Distributed workers receive key material in memory;
            use HTTPS for their controller connection.
          </p>
          <button type="button" onClick={upload}>
            {busy ? "Validating & saving…" : "Save certificate profile"}
          </button>
          <button
            type="button"
            className="text-button"
            onClick={() => {
              setFiles({});
              setFileGeneration((previous) => previous + 1);
              setError("");
              setName("");
            }}
          >
            Clear selected files
          </button>
        </div>
      )}
      {error && (
        <div className="notice error" role="alert">
          {error}
        </div>
      )}
      {message && (
        <p className="notice success" role="status">
          {message}
        </p>
      )}
    </fieldset>
  );
}
