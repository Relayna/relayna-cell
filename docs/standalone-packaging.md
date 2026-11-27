# Build Relayna Cell for a standalone Linux VM

## CI packages

The **Cell standalone packages** workflow builds Linux AMD64 and ARM64 bundles
when a `cell-vMAJOR.MINOR.PATCH` tag is pushed. It also supports **Run workflow**
in the Actions tab once the workflow is on the default branch. Choose a branch
to verify its commit or an existing Cell version tag to rebuild that version.
Tagged artifacts use the tag in their name. Manual branch builds use the commit
SHA. A rerun has its own attempt number. GitHub Actions retains artifacts for
30 days.

On the workflow run page, download and unzip the artifact matching the guest
architecture. Use AMD64 for `x86_64` and ARM64 for `aarch64`. The archive and its
checksum are inside the artifact. For example, on ARM64 Linux, from the
unzipped artifact directory:

```sh
sha256sum -c relayna-cell-linux-arm64.tar.gz.sha256
tar -xzf relayna-cell-linux-arm64.tar.gz
```

For a fresh VM, check out the same tag or commit as the artifact and prepare
the host by following the [Linux setup guide](linux-setup.md). From that
checkout, keep the tagged setup scripts and use the extracted binaries:

```sh
sudo bash scripts/setup-linux.sh --engine docker --inference none \
  --bin-dir /absolute/path/to/relayna-cell-linux-arm64/bin
sudo install -m 0755 /absolute/path/to/relayna-cell-linux-arm64/bin/cell-task \
  /opt/relayna-cell/bin/cell-task
sudo /opt/relayna-cell/bin/cell health
```

## Local packages

Use a clean checkout of the revision you want to test. Build the Linux binaries
and a checksum locally with Go 1.26 or later.

```sh
bash scripts/package-standalone.sh arm64 ./bin/standalone
# Use amd64 for an x86_64 Linux VM.
```

The archive contains `cell-server`, `cell-init`, `cell`, `cell-task`, the
systemd unit, the gVisor trace configuration, and both SDK example images'
build inputs. Verify the archive with `sha256sum -c` or `shasum -a 256 -c`.
The VM still needs a configured rootful Docker or Podman runtime, gVisor, and
Cell's host network guard. Follow the matching [Linux setup guide](linux-setup.md)
before installing the binaries beneath `/opt/relayna-cell/bin`. Run the server
on that dedicated host and use the operator socket for task launch.

The optional launcher container packages only the `cell-task` client. Build
it from the committed source to exclude local files from the Docker context.

```sh
git archive HEAD | docker build -f deploy/standalone/Dockerfile -t relayna-cell/task-launcher:local -
```

Use the launcher only on a trusted host with an already running Cell server.
Mount the root-only operator socket where the launcher expects it. The
launcher can run with a read-only filesystem and networking disabled.

```sh
printf '%s\n' '{"task_id":"demo","attempt_id":"attempt-1","payload":{"value":"hello"}}' |
  sudo docker run --rm -i --network none --read-only \
    --mount type=bind,src=/run/relayna-cell/api.sock,dst=/run/relayna-cell/api.sock \
    relayna-cell/task-launcher:local --image relayna-cell/sdk-task:local
```

Build the workload image on the Cell server's engine using the
[SDK example](../examples/relayna-sdk-task/README.md). The daemon
keeps the task inference configuration and credentials on the VM. The launcher
container only needs the socket and its fixed arguments.
