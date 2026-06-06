# btr — BuildKit Task Runner

A lightweight, for-fun task runner that uses [BuildKit](https://github.com/moby/buildkit) as its execution engine.

Declare tasks in YAML, and each task is compiled into a **BuildKit LLB (Low-Level Build definition) state**
and sent to `buildkitd` for execution. Dependency resolution, parallel execution, and caching are all
offloaded to the BuildKit engine.

## How it works

- Each task is an LLB node that "runs a list of commands on top of a given image".
- A dependency (`deps`) is an **ordering edge** created by mounting the dependency's resulting state read-only.
- Shared dependencies are built **only once** (via memoization), and independent branches are
  **executed in parallel automatically** by BuildKit.

## Task definition (tasks.yaml)

```yaml
tasks:
  deps:
    image: golang:1.26-alpine
    cmds: ["echo downloading", "sleep 1"]
  lint:
    image: golang:1.26-alpine
    deps: [deps]
    cmds: ["echo linting"]
  build:
    image: golang:1.26-alpine
    deps: [deps]
    cmds: ["echo building"]
  all:
    image: alpine:3.20
    deps: [lint, build]   # lint and build run in parallel; deps runs only once
    cmds: ["echo done"]
```

| Field   | Required | Description |
|---------|:--------:|-------------|
| `image`  | ✓        | Base image (its config, e.g. `PATH`, is applied automatically) |
| `cmds`   | ✓        | Commands to run in order (at least one) |
| `deps`   |          | Tasks that must complete first |
| `env`    |          | Environment variables |
| `dir`    |          | Working directory (defaults to `/src` when `source` is set) |
| `source` |          | Local directory mounted read-only into the container |
| `caches` |          | Container paths backed by a shared persistent cache (e.g. `/go/pkg/mod`) |

## Usage

### 1. Start buildkitd (over TCP)

```sh
docker run -d --name buildkitd --privileged -p 1234:1234 \
  moby/buildkit:latest --addr tcp://0.0.0.0:1234
export BUILDKIT_HOST=tcp://127.0.0.1:1234
```

### 2. Build & run

```sh
go build -o btr ./cmd/btr
./btr list -f examples/tasks.yaml
./btr run all -f examples/tasks.yaml
```

The target address is resolved in this order: the `--addr` flag → the `BUILDKIT_HOST` environment
variable → the default unix socket.

## Dogfooding

`btr` runs its own dev tasks (defined in the root [`tasks.yaml`](./tasks.yaml)). Each task mounts the
project source read-only at `/src` and shares Go's module and build caches across tasks/runs via
persistent cache mounts, so repeated runs are fast.

```sh
go build -o btr ./cmd/btr
./btr run ci      # fmt + vet + build + test, run in parallel
```

## Roadmap

- Exporting build artifacts to the local filesystem (`SolveOpt.Exports`)
- TUI progress display via progressui
- `btr graph` (DOT output), variable expansion, cache import/export
