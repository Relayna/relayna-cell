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
