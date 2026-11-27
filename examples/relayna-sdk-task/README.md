# Run one Relayna SDK task in a Cell

`cell-task` is a trusted local launcher for a single task attempt. The SDK worker
keeps its broker connection, owns task delivery and retries, and publishes the
terminal status and result. Cell runs the handler in an offline gVisor container
and records its execution and cleanup.

Run these commands on the Linux host or VM running Cell. The launcher needs
access to the root-only operator socket. Treat any process with that socket
access as a Cell operator. An SDK worker can use an operator-configured launcher
that runs this binary with fixed arguments. Keep the launcher, image, socket
path and timeout in trusted deployment configuration.

```sh
go build -o ./bin/cell-task ./cmd/cell-task
sudo docker build -t relayna-cell/sdk-task:local examples/relayna-sdk-task
printf '%s\n' '{"task_id":"demo-1","attempt_id":"attempt-1","payload":{"value":"hello"}}' |
  sudo ./bin/cell-task --image relayna-cell/sdk-task:local
```

Use a digest-pinned image reference or an image ID for a fixed deployment. The
reference image returns `{"echo":{"value":"hello"}}` for the input above. Use the
same container engine to build or load the image as your Cell server uses.

The worker invokes an absolute launcher path with a fixed argument vector and
sends one UTF-8 JSON object on stdin, then closes stdin. The three required
fields are `task_id`, `attempt_id` and object-valued `payload`. Each ID is an
opaque string of 1–256 UTF-8 bytes and the entire request is limited to 256 KiB.
The deployment selects the image with `--image`, can override the local socket
with `--socket`, and can set `--timeout` in seconds from 1 to 300 (default 60).

The image contains `/opt/relayna-cell/task` as an executable. Cell calls it
directly with no arguments, under the standard unprivileged session profile.
The executable reads just the payload object on stdin and writes one UTF-8
JSON object of up to 256 KiB to stdout. It can use stderr for diagnostics.
Cell's existing execution audit captures workload stdout and stderr. Keep
sensitive data out of those streams. The launcher passes no host environment,
broker connection, mounts or network grants to the workload.

Exit code 0 means execution produced valid JSON and the Cell reached a clean
deleted state. The launcher writes one response to stdout.

```json
{
  "status": "completed",
  "task_id": "demo-1",
  "attempt_id": "attempt-1",
  "cell_id": "<Cell ID>",
  "execution_id": "<Cell execution ID>",
  "result": {"echo": {"value": "hello"}}
}
```

Exit code 1 reports failure. For a valid invocation the JSON response has
`status: "failed"` and an `error` string, with Cell/execution IDs when known.
A configuration or input error reports a diagnostic on stderr. A forcibly
terminated process can leave the worker without a complete response. The worker
should require exit code 0, a matching task and attempt, and a valid completed
response before publishing a successful result.

The timeout bounds startup and execution and sets the server-side Cell lifetime.
The launcher attempts deletion after success, failure, timeout, SIGINT or
SIGTERM, with its own cleanup deadline of 35 seconds. Allow the configured
timeout plus cleanup and process startup in the SDK subprocess deadline. If
creation loses its response or the launcher is killed before cleanup, the
server-side expiry still applies. A failed or unconfirmed cleanup suppresses a
successful result.

The SDK can map `attempt_id` to the status envelope and put `cell_id` and
`execution_id` under `meta.cell`. For a normal response, use the known Cell ID to
inspect the audit with `sudo ./bin/cell events CELL_ID`. Each Cell's
trusted `cell.requested` event also carries its generated name. That name is
`sdk-task:` followed by the lowercase hexadecimal SHA-256 of
`<task byte length>:<task ID><attempt byte length>:<attempt ID>`, assembled
in UTF-8 with decimal lengths. This identifies an attempt without putting its
raw IDs in the Cell name. Invoking the launcher twice creates two Cells even
when the task and attempt IDs match. The SDK retains responsibility for
delivery deduplication and retry policy.

## Opt in to Gateway inference

Start the Cell server with
`--task-inference-config /etc/relayna-cell/task-inference.json`. The config and
both credential files must be owner-only regular files owned by the daemon
user. Store them under an operator-controlled directory. Restart the daemon
when rotating the files.

```json
{
  "gateway_url": "https://gateway.example/v1/chat/completions",
  "model": "approved-model",
  "task_service": "reference",
  "gateway_key_file": "/etc/relayna-cell/task-gateway.key",
  "worker_token_file": "/etc/relayna-cell/task-worker.token"
}
```

Configure the Gateway virtual key for managed `/v1/chat/completions`, its
configured provider, and exactly the approved model and task registration.
Keep streaming and tools disabled. Use a separate virtual key for external
task submission. The worker-token file contains the existing Gateway
`RELAYNA_WORKER_TOKEN` value. Cell injects both credentials and the task,
attempt and registration headers from the host for its fixed destination.
The Gateway URL uses HTTPS, with HTTP accepted for literal loopback addresses
in local integration tests.

```sh
sudo docker build -t relayna-cell/sdk-task-inference:local \
  -f examples/relayna-sdk-task/Dockerfile.inference examples/relayna-sdk-task
printf '%s\n' '{"task_id":"demo-2","attempt_id":"attempt-1","payload":{"model":"approved-model","prompt":"Say hello."}}' |
  sudo ./bin/cell-task --inference --image relayna-cell/sdk-task-inference:local
```

An opted-in task uses `POST http://task-inference.cell/v1/chat/completions`
through the Cell HTTP proxy. That session only permits this destination and
model. Requests are nonstreaming and cannot use tool options. Task and attempt
IDs use 1–256 ASCII letters, digits, periods, underscores, tildes or hyphens,
starting with a letter or digit. The default offline invocation is unchanged.

After cleanup the launcher projects up to 64 trusted journal decisions in
`inference: {events, truncated}`. Events carry `event_id`, `timestamp`,
`decision` (`allow`, `deny`, `result` or `error`), and optional `status`
(Gateway HTTP status) and fixed `reason` code. The SDK can copy the object to
`meta.cell.inference` for Studio. Cell records the task and attempt on its
trusted `cell.requested` event and uses the Cell ID to join decisions. Summary
events omit request and response bodies, raw headers and credentials.

## Select a reusable task profile

Put the catalog under an operator-controlled directory and set its mode to
`0600`. Select the profile in the trusted launcher command. Task input
continues to contain only the task ID, attempt ID, and payload.

```json
{
  "profiles": {
    "reports": {
      "image": "relayna-cell/report-worker:local",
      "cpu": 1,
      "memory_mib": 256,
      "workspace_mib": 32,
      "pids": 32,
      "timeout_seconds": 60,
      "inference": false,
      "artifacts": ["reports/summary.json"]
    }
  }
}
```

Omitted resource settings use the standard launcher defaults. Inference is
off by default. A profile that enables it uses the server's existing scoped
inference configuration. When the SDK already passes image, timeout or
inference flags, the launcher checks that they match the profile.

```sh
sudo install -d -m 0700 /var/lib/relayna-cell/task-artifacts
printf '%s\n' '{"task_id":"report-1","attempt_id":"first","payload":{}}' |
  sudo ./bin/cell-task --profiles /etc/relayna-cell/task-profiles.json \
    --profile reports --artifact-dir /var/lib/relayna-cell/task-artifacts
```

For SDK deployments, configure a wrapper to select the profile and private
output directory, then pass through the SDK's fixed flags.

```sh
#!/bin/sh
exec /usr/local/bin/cell-task --profiles /etc/relayna-cell/task-profiles.json \
  --profile reports --artifact-dir /var/lib/relayna-cell/task-artifacts "$@"
```

The workload writes its requested files under `/workspace`. Each entry is a
required relative file path. The launcher downloads at most eight files,
limited to 4 MiB each and 16 MiB total. It publishes the bundle only after
execution succeeds and deletion is confirmed. Failure to fetch a required
file fails the attempt. The attempt response carries `profile` and the
relative `artifact_manifest` path. The manifest identifies the task, attempt,
Cell, and execution and lists each file's path, byte count and SHA-256.
Artifact bytes are available under `files/` alongside the manifest. An
operator can retrieve the bundle on the launcher host. Size limits apply per
attempt. Set retention on the private output volume for long-running services.

## Archive audit exports

Provision a private archive directory outside the server's hot `logs`
directory, ideally on a volume with independent retention.

```sh
sudo install -d -m 0700 /var/lib/relayna-cell-audit
sudo cell-server --audit-archive-dir /var/lib/relayna-cell-audit \
    --audit-hot-retention 24h
```

The server checks at startup and every minute. Once a segment is old enough,
the server durably copies it to the archive before removing it from the hot
export directory. Preserve enough hot history for Alloy to catch up.
SQLite keeps the authoritative events, so `cell events` and replay continue
to work after archival. Back up the database and archive volume according to
the evidence retention period you need. Manually generated replay files are
managed by the operator.
