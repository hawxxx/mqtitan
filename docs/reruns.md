# Rerunning tests

Open a stopped, completed, failed or interrupted test under **Tests**, then click **Run again**. The dashboard opens the new live run. Starting disables the button; errors remain on the original result page.

A rerun creates a new test ID and `sourceTestId` link to its source. The old run, samples and reports are unchanged. It uses the original scenario and starts from its first stage—not from the last slider settings. Counters, latency histograms, threshold results, load-change history and manual overrides reset.

The controller loads the encrypted original configuration rather than the redacted YAML shown in the browser. MQTT credentials and certificate-profile references are reused without sending secrets to the UI. Certificate profiles, payload files and other external assets must still be available.

New tests persist `workerIds`: an empty array means local execution; a populated array records distributed workers. Reruns reuse those IDs and fail if workers are unavailable or assigned elsewhere. They never silently fall back from a recorded distributed assignment to local execution. Start a reviewed scenario in **Scenarios** to choose different workers.

Older runs may have no worker metadata (`workerIds: null`). The UI explicitly warns that **Run again** uses the local engine for these records. This is not automatic recovery of an old distributed assignment.

Only one test may run at a time on a controller. An active source cannot be rerun, and any other active run prevents launch. Finishing and stopping remain terminal operations; rerunning does not resume MQTT sessions or paused execution.

## API

```http
POST /api/v1/tests/TEST_ID/rerun
```

No request body is required. The endpoint uses the same authentication and origin protections as test creation. It returns HTTP 201 with the new, redacted test object. HTTP 404 means the source is missing; 409 means the source has not finished or another test is running; 400 means its scenario, certificate profiles or worker assignment cannot be reused. Error responses never echo the stored raw configuration.
