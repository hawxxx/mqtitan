# Certificates in the web UI

1. Open **Create test** and enter a secure broker URL, for example `wss://mqtt.example.com:443/mqtt` or `mqtts://mqtt.example.com:8883`.
2. The **TLS & certificates** section appears. Publicly trusted endpoints can use **System trust** without uploading anything.
3. Select **Upload certificates**, enter a profile name, and choose the required PEM files:
   - CA bundle: optional custom CA certificates for server verification.
   - Client certificate and private key: optional mTLS credentials; supply both together. The certificate file can contain its intermediate chain.
4. Select **Save certificate profile**. The server validates the files and automatically selects the new profile.
5. Optionally set **Server name / SNI**. Leave it empty to verify the hostname in the broker URL.
6. Review the generated scenario, then run it locally or select distributed workers.

The UI prevents review while files remain selected but unsaved. Use **Clear selected files** to discard them. Existing profiles can be reused in subsequent tests.

## Scenario representation

```yaml
broker:
  url: wss://mqtt.example.com:443/mqtt
  version: "5"
  tls:
    profile: 0123456789abcdef01234567
    serverName: mqtt.example.com
```

Use an actual profile ID from your controller. Profile references are controller-specific, not portable secret exports. For CLI execution, submit the scenario with `--controller` pointing to that controller. Standalone CLI runs on another database need local `caFile`, `certFile` and `keyFile` settings instead. Do not combine profile references with local file paths or inline PEM fields.

## Security and limits

- Files must contain PEM data, at most 128 KiB per file. PKCS#12/PFX and encrypted private-key files are not supported.
- CA bundles must contain CA certificates. Expired/not-yet-valid certificates, malformed bundles, mismatched keys and incompatible client-certificate usage are rejected.
- Profiles are encrypted with the controller's existing AES-GCM storage. Back up the database and its `.key` file together and restrict access to both.
- The UI keeps selected files only in memory. It does not save them to browser storage or place private keys in the YAML editor. File selections are cleared after a successful upload.
- Listing/upload responses return only profile metadata, including fingerprint and expiry. Private keys are never downloadable through the certificate API or included in test reports.
- Controller and trusted workers necessarily handle decrypted credentials in memory to establish TLS connections. Workers receive resolved PEM data over their authenticated controller connection, not local filesystem paths. **Use HTTPS for remote dashboard and worker/controller traffic.** Browser uploads over non-loopback HTTP are blocked; configure TLS termination at the reverse proxy for remote installations.
- These profiles share the controller's administrative authentication boundary; there is no per-user profile isolation/RBAC. Treat every authorized worker and administrator as trusted with load-test credentials.
- TLS verification remains enabled. Do not use certificate uploads to bypass server-name validation.

If the AWS load balancer terminates TLS, client TLS credentials authenticate to that load balancer. Configure the load balancer/HAProxy separately for backend TLS or certificate forwarding. The tool does not configure or automatically bridge those trust boundaries.

## API

`POST /api/v1/certificates` accepts `name`, optional `caPem`, and optional paired `certPem`/`keyPem`. `GET /api/v1/certificates` lists metadata only. Both use the existing control-plane authentication. Material is not exposed by a GET endpoint. Create a new profile when rotating certificates; profile update/delete is not currently implemented.
