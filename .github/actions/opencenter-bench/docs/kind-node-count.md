# Why the Kind deploy failed, and what it was not

openCenter's Kind deploy was attempted twice and failed both times. The second
attempt reached control-plane init and died after 4m17s on:

```
error execution phase wait-control-plane: failed while waiting for the kubelet
to start: ... http://127.0.0.1:10248/healthz: context deadline exceeded
```

This was written up at the time as "probably environmental (WSL), unproven".
That was a guess, and it was wrong in its specifics. Here is the measurement.

## What was measured

Each probe used plain `kind` with no openCenter involved, on this machine,
while the existing `mockbank` cluster (3 nodes, `kindest/node:v1.35.0`) kept
running. Every probe deleted its own cluster; `kind get clusters` returned
`mockbank` alone before and after each one.

| # | Cluster asked for | Result |
|---|---|---|
| 1 | kind's defaults — v1.36.1, 1 node | **worked in 34s** |
| 2 | openCenter's shape — v1.35.0, 1 control plane + 2 workers | **failed at 251s** |
| 3 | v1.35.0, 1 control plane, no workers | **worked in 34s** |
| 4 | v1.36.1, 1 control plane + 2 workers | **failed at 70s** |

## What that rules out

- **Not openCenter's orchestration.** Probe 2 failed with plain `kind`, using
  the same shape openCenter asks for. openCenter is not driving kind wrongly.
- **Not the pinned image.** Probe 3 built `kindest/node:v1.35.0` in 34
  seconds. Probe 4 failed on kind's own newer image.
- **Not WSL as such, nor cgroups, nor memory.** A single-node cluster builds
  here reliably. This host runs cgroup v2, not v1, with 13.9 GB free and 12
  CPUs. The first version of the diagnosis rule in `cmd/testlab/diagnose.go`
  blamed cgroup v1 and low memory; both were wrong, and it has been corrected.

## What it leaves

The node count. Three nodes fail at either version; one node succeeds at
either version. The relevant limit on this host:

```
fs.inotify.max_user_instances = 128     # kind's guide asks for 512
fs.inotify.max_user_watches   = 524288  # already fine
```

`mockbank` is already running three nodes and holding instances against that
budget of 128. Starting three more exhausts it, and the symptom is a kubelet
or a bootstrap client that times out rather than an error naming the limit —
which is why the message was so unhelpful. Probes 2 and 4 produced two
different timeout messages for what is one cause.

## The one step not done

Raising the limit and rebuilding needs root, and `sudo` wants a password in a
non-interactive shell. To close it:

```bash
sudo sysctl -w fs.inotify.max_user_instances=512
# then retry the three-node cluster, or openCenter's deploy
```

If it then builds, the chain above is complete. Until someone runs it, the
inotify budget is a strongly-evidenced cause rather than a proven one — the
node count is proven, the mechanism is not.

## What the test bench does with this

`scripts/kind-cluster.sh` builds the cluster the bench tests against, and the
console has a button for it in the Kind environment. Both follow the table
above rather than openCenter's defaults:

- **One node.** It builds in about 32 seconds here, every time.
- **A free port between 6450 and 6520**, never 6443, so an existing cluster is
  not disturbed.
- **Its own name**, `opencli-testbench`, and `kind delete cluster` is only
  ever called with `--name`. Every other cluster on the machine belongs to
  someone else. This was tested: removing it left `mockbank` running with all
  three of its containers up.
- **A preflight** that reads `fs.inotify.max_user_instances` and counts
  running kind nodes, and says what to do — rather than letting a build hang
  for four minutes and report a kubelet timeout.

```
./start.sh --kind                # build it, then serve
scripts/kind-cluster.sh up       # or on its own
scripts/kind-cluster.sh status
scripts/kind-cluster.sh down
```

The kubeconfig is passed into the sandbox, so commands reach the cluster:
`opencenter cluster doctor testbench/tb-kind` reports `kubectl: OK` and exits
0. Only this cluster's kubeconfig is offered — the ambient `KUBECONFIG` could
point anywhere, including production.

## What openCenter could do about it

Two things, independent of this machine:

1. **The default is heavier than the job needs.** `internal/config/v2/defaults.go`
   sets `kindDefaultWorkerCount = 2`. A local Kind cluster used to exercise a
   CLI lifecycle does not need two workers; one node exercises every stage.
2. **The error tells you nothing.** "waiting for the kubelet to start" is the
   symptom furthest from the cause. Checking `fs.inotify.max_user_instances`
   and counting running kind nodes before starting, and saying so when it
   fails, would turn a four-minute mystery into a one-line message.

The related port defect is already recorded: openCenter hardcodes the Kind API
server port to 6443, so a second cluster collides with the first and surfaces a
raw docker "port is already allocated".
