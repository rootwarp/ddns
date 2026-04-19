---
type: document
status: draft
tags:
  - dev/go
  - infra/dns
  - dev/architecture
PARA: projects
---

**Connections:**
- [[prd]]
- [[project-plan]]
- [[README]]

# Software Architecture: `ddns`

## 1. Executive Summary

`ddns` is a Go 1.22 daemon that reconciles a Google Cloud DNS `A` record against the host's current public IPv4. The core design metaphor is **poll-resolve-reconcile**: a single `time.Ticker`-driven loop (a) resolves the public IP from ≥2-of-3 HTTPS echo services, (b) compares the result against a local state file *and* the live Cloud DNS record, and (c) issues a single atomic `Changes.Create` only when both comparisons show drift. Key invariants: (1) no update is ever sent with less than quorum agreement across echo services; (2) no update is sent without first re-reading the live record, so manual `gcloud` edits are respected on the next tick; (3) the state file is written atomically (temp + rename, `0600`) so a power cut mid-write cannot corrupt it; (4) the provider layer is a `DNSProvider` interface, not a direct dependency on the Google Cloud library, so the v1 single-provider decision does not leak into the daemon loop; (5) credentials are never touched by `ddns` itself — ADC is delegated end-to-end to the `google.golang.org/api/dns/v1` client.

## 2. Module Boundaries

**Packaging decision: single Go module (`github.com/rootwarp/ddns`) with internal packages, not a multi-module layout.** v1 produces exactly one binary; the package boundary exists to keep the daemon loop testable against a fake `DNSProvider` and fake `IPResolver`, not to publish reusable libraries. If the provider interface ever grows into its own library surface (e.g., absorbed by a larger multi-cloud project), the `internal/dnsprovider` contract is written to be extraction-ready.

### Module summary

| Package | Responsibility (one sentence) | Owns data | Depends on | v1? |
|---|---|---|---|---|
| `cmd/ddns` | `main`: parse flags via `urfave/cli/v3`, load config, wire up resolver + provider + state + daemon, run the selected subcommand. | — | `urfave/cli/v3`, all internal pkgs | yes |
| `internal/config` | Load and validate YAML config from `~/.config/ddns/config.yaml`; expand `~` and env vars in paths. | `Config`, `RecordConfig`, `ResolverConfig` structs | `gopkg.in/yaml.v3`, `net/netip` | yes |
| `internal/resolver` | Query configured HTTPS echo services in parallel and return the quorum IPv4 address. | `ResolvedIP` value type | `net/http`, `context`, `net/netip` | yes |
| `internal/dnsprovider` | Provider-agnostic interface (`Get`, `Upsert`) plus the Google Cloud DNS implementation. | `Record` value type | `google.golang.org/api/dns/v1`, `golang.org/x/oauth2/google` | yes |
| `internal/state` | Atomically read/write `state.json`; survive power-loss mid-write. | `State` struct on disk | `os`, `encoding/json`, `fs_atomic` helper | yes |
| `internal/daemon` | Orchestrate one reconciliation pass; drive the polling loop under a `context.Context`; handle signals and backoff. | `Daemon` struct | `resolver`, `dnsprovider`, `state`, `slog` | yes |
| `internal/logging` | Construct the `slog.Logger` (text or JSON), apply default attrs (pid, version, record name). | — | `log/slog` | yes |
| `internal/fs_atomic` | Shared `WriteFile` helper that writes via tempfile + `os.Rename` with explicit `0600`. | — | `os`, `path/filepath` | yes |
| `internal/health` | Optional HTTP server for `/healthz`, `/readyz`, `/state`, `/metrics`. | — | `net/http`, `state`, `daemon` | P1 stub |
| `internal/version` | Compile-time `ldflags`-injected `Version`, `Commit`, `BuildDate`. | — | — | yes |

### Package: `cmd/ddns`

Subcommand dispatch (`run`, `sync`, `status`, `version`) is done via `github.com/urfave/cli/v3`. The rationale: urfave/cli owns flag parsing, help-text generation, subcommand routing, and `context.Context` threading, which removes four mechanical concerns from `main`. v3 specifically is chosen over v2 because its `cli.Command.Run(ctx, args)` signature matches how the daemon already wants to receive context from `signal.NotifyContext`, and because v3's command tree is a plain struct literal, not the slice-of-pointers pattern v2 uses.

The `main` function does three things: (1) build the root `cli.Command` struct literal with global flags (`--config`, `--log-format`) and the four subcommands; (2) wrap the process context with `signal.NotifyContext(ctx, SIGINT, SIGTERM)` so every subcommand honors Ctrl-C; (3) call `cmd.Run(ctx, os.Args)` and translate any returned error into an exit code via a small `exitCodeFor(err)` function that maps `ErrNoQuorum → 1`, `ErrTransient|ErrAuth → 2`, `ErrConfig → 3`, anything else → 2.

Each subcommand handler (`runAction`, `syncAction`, `statusAction`, `versionAction`) lives in its own file under `cmd/ddns/` and has the signature `func(ctx context.Context, cmd *cli.Command) error`. Handlers never write logs directly — they defer all logging to `internal/logging` after config load. The `version` handler is the one exception: it does not require config, reads from `internal/version`, and prints to stdout directly.

Representative skeleton:

```go
func main() {
    cmd := &cli.Command{
        Name:  "ddns",
        Usage: "Dynamic DNS updater for Google Cloud DNS",
        Flags: []cli.Flag{
            &cli.StringFlag{Name: "config", Usage: "path to config file", Sources: cli.EnvVars("DDNS_CONFIG")},
            &cli.StringFlag{Name: "log-format", Value: "auto", Usage: "auto|text|json"},
        },
        Commands: []*cli.Command{
            {Name: "run", Usage: "run the reconciliation daemon", Action: runAction, Flags: runFlags},
            {Name: "sync", Usage: "run one reconciliation pass and exit", Action: syncAction, Flags: syncFlags},
            {Name: "status", Usage: "print last-known state", Action: statusAction, Flags: statusFlags},
            {Name: "version", Usage: "print version", Action: versionAction},
        },
    }
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()
    if err := cmd.Run(ctx, os.Args); err != nil {
        os.Exit(exitCodeFor(err))
    }
}
```

### Package: `internal/config`

```go
type Config struct {
    PollInterval time.Duration   `yaml:"poll_interval"` // default 5m
    StatePath    string          `yaml:"state_path"`    // default ~/.local/state/ddns/state.json
    LogFormat    string          `yaml:"log_format"`    // "auto" | "text" | "json"
    Resolver     ResolverConfig  `yaml:"resolver"`
    Records      []RecordConfig  `yaml:"records"`
    HealthAddr   string          `yaml:"health_addr"`   // P1, empty = off
}

type ResolverConfig struct {
    Sources []string `yaml:"sources"` // default: ipify, ifconfig.me, icanhazip
    Quorum  int      `yaml:"quorum"`  // default: 2
    Timeout time.Duration `yaml:"timeout"` // default: 10s
}

type RecordConfig struct {
    Project     string `yaml:"project"`      // GCP project ID
    ManagedZone string `yaml:"managed_zone"` // Cloud DNS managed-zone name
    Name        string `yaml:"name"`         // FQDN, trailing dot tolerated
    TTL         int64  `yaml:"ttl"`          // seconds, default 300
    Type        string `yaml:"type"`         // "A" only — IPv4 records
}
```

Validation, performed at load time and returning a single `*ConfigError` that aggregates all failures:

- `Records` must be non-empty.
- Every `Name` parses as a valid DNS name and is normalized to trailing-dot form for the provider API.
- Every `Project` and `ManagedZone` is non-empty.
- `Resolver.Quorum` is in `[1, len(Resolver.Sources)]` and `Resolver.Sources` has at least 2 entries when `Quorum >= 2`.
- `TTL` is in `[30, 86400]`.
- `Type` is `"A"`. Any other value is rejected with a clear "only A records are supported" message. There is no plan to support `AAAA` in a future release.

### Package: `internal/resolver`

```go
type IPResolver interface {
    Resolve(ctx context.Context) (netip.Addr, ResolveReport, error)
}

type ResolveReport struct {
    Sources   []SourceResult // one per configured echo service
    Quorum    int            // count of agreeing sources for the returned IP
    ElapsedMs int64
}

type SourceResult struct {
    URL   string
    IP    netip.Addr  // zero on error
    Error string      // empty on success
}
```

Implementation: fire one `http.Get` per source in parallel under the resolver timeout (default 10s) using a shared `http.Client` with a 5s `TLSHandshakeTimeout` and `DisableKeepAlives: true` (each tick's small request volume does not benefit from connection reuse, and disabled keep-alives avoid the dead-socket ambiguity that residential NATs sometimes introduce). Each response body is read up to 64 bytes (bounded to guard against a misbehaving service), whitespace-trimmed, and parsed with `netip.ParseAddr`. Non-IPv4, private-range, loopback, link-local, and multicast addresses are rejected per-source with a `sanitize_reject` reason. After all sources return or timeout, the IP with the highest agreement count is selected; if that count is below `Quorum`, the resolver returns `ErrNoQuorum` along with the full `ResolveReport` for logging.

### Package: `internal/dnsprovider`

```go
type DNSProvider interface {
    Get(ctx context.Context, r RecordRef) (Record, error)
    Upsert(ctx context.Context, r RecordRef, new Record) (UpsertResult, error)
}

type RecordRef struct {
    Project     string
    ManagedZone string
    Name        string // trailing-dot form
    Type        string // "A"
}

type Record struct {
    Rrdatas []string // single value in v1 (one IPv4)
    TTL     int64
}

type UpsertResult struct {
    Changed bool // false when Upsert was a no-op because Get returned equal state
    OldRrdatas []string
    NewRrdatas []string
}
```

The Google Cloud DNS implementation lives in `internal/dnsprovider/gcp`. It constructs an `*dns.Service` via `dns.NewService(ctx, option.WithScopes(dns.NdevClouddnsReadwriteScope))`, relying on ADC under the hood. `Get` calls `ResourceRecordSets.Get`, translating a 404 into `(Record{}, ErrNotFound)` so the daemon can distinguish "record is missing, create it" from "we could not reach the API". `Upsert` does a final equality check before constructing a `Changes.Create` payload that atomically deletes the old RRset (using the `Get` result as the precise deletion target) and adds the new one in the same change; this is the Cloud DNS-native way to do an atomic swap and avoids a dangling-record window that a naive "delete then add" would introduce.

The `Upsert` path refuses to issue a write when `new.Rrdatas` is empty, contains a non-IPv4 string in v1, or when `RecordRef.Type != "A"` — these are internal invariants, so the checks assert with an error rather than silently coercing.

### Package: `internal/state`

```go
type State struct {
    LastCheckedAt  time.Time `json:"last_checked_at"`
    LastObservedIP string    `json:"last_observed_ip"`
    LastUpdatedAt  time.Time `json:"last_updated_at,omitempty"`
    LastResult     string    `json:"last_result"`     // "noop" | "updated" | "error"
    LastError      string    `json:"last_error,omitempty"`
    RecordName     string    `json:"record_name"`
}
```

`Load` returns the current state or `(State{}, ErrNotFound)` when the file does not exist; the daemon treats `ErrNotFound` as equivalent to an empty state (first run). `Save` writes via `internal/fs_atomic.WriteFile` — tempfile in the same directory, `fsync`, `os.Rename`, `fsync` of the parent dir — and enforces `0600`.

When there are multiple records in the config, each gets its own state file: `state.json` becomes `state/<sha256(record-name)>.json` under the configured `state_path` directory. This keeps the per-record state independent and makes a future "reload one record" hot path easy.

### Package: `internal/daemon`

```go
type Daemon struct {
    cfg       *config.Config
    resolver  resolver.IPResolver
    providers map[string]dnsprovider.DNSProvider // keyed by provider id, currently always "gcp"
    store     state.Store
    log       *slog.Logger
    clock     clock.Clock                        // injectable for tests
    backoff   *backoff.Exponential
}
```

A tick is:

1. `resolver.Resolve(ctx)` — on `ErrNoQuorum`, log `resolver_no_quorum` with the report, record an error state, and return.
2. For each configured record: `dnsprovider.Get(ctx, ref)` to fetch the current live value. On transient error, log and bump the backoff on the next tick; state is updated with `last_result=error` and the error message.
3. Compare `quorumIP` against `state.LastObservedIP` and `live.Rrdatas[0]`. If both equal, emit `reconcile_noop` and return.
4. If either differs, and we are not in `--dry-run`, call `dnsprovider.Upsert(ctx, ref, Record{Rrdatas: []string{quorumIP.String()}, TTL: cfg.TTL})`.
5. On success, write updated state with `last_result=updated` and log `reconcile_updated` (old IP → new IP, record name, TTL). On failure, log `reconcile_error` and update state accordingly.

The `run` subcommand wraps this in a `for` loop driven by a `time.Ticker(cfg.PollInterval)`; the `sync` subcommand executes exactly one tick and returns its outcome as an exit code (0 / 1 / 2 / 3 per PRD).

Backoff is applied at the daemon level, not the provider level: after N consecutive provider errors, the next tick is delayed by `min(baseInterval * 2^(N-1), 30m)`. The backoff resets on the next success, not on any success — i.e., a successful resolve without a successful provider call is not enough to reset it.

### Package: `internal/logging`

`log/slog` with a small wrapper: default attrs are `version`, `pid`, and `record` (populated per-tick). Format selection: `auto` picks text on a TTY (detected via `isatty(os.Stdout.Fd())` without a third-party dep, using the stdlib `term.IsTerminal`) and JSON otherwise. Level is always `INFO` in v1; a future `--debug` flag is a P1 consideration.

### Package: `internal/fs_atomic`

Single public function: `WriteFile(path string, data []byte, mode fs.FileMode) error`. Implementation: create a tempfile in the same directory as `path` with `os.CreateTemp`, write all bytes, `Chmod` to `mode`, `Sync`, `Close`, `Rename` to `path`, `fsync` the parent directory. Used by `config` (for atomic upgrades on migration, if any), by `state`, and anywhere else that must not leave a half-written file.

## 3. Core Flows

### 3.1 Cold start (`ddns run`)

1. Load config; bail early on validation errors with exit 3.
2. Build `slog.Logger`; emit `startup` with version, config path, poll interval.
3. Build `IPResolver` and `DNSProvider`. Provider construction is lazy in the sense that it returns an error only if ADC cannot be discovered; it does not call Cloud DNS until the first tick. Startup exits 3 on ADC failure.
4. Load state (empty if not present). Log `state_loaded` with the last-observed IP and timestamp.
5. Start the ticker at `t=0` (an immediate first tick, not one `poll_interval` later) so the daemon reconciles promptly after a reboot.
6. Enter the loop. First signal (`SIGTERM`/`SIGINT`) cancels the root context, the in-flight tick completes or is cancelled at the next HTTP roundtrip, state is flushed, and the binary exits 0.

### 3.2 IP change detected

1. Tick fires at `t=N`. Resolver returns `192.0.2.42` with quorum 3/3. Resolver report logged.
2. Provider `Get` returns the live record rrdata `["198.51.100.7"]`, TTL 300.
3. Quorum IP differs from live rrdata; daemon constructs `Changes.Create` with `deletions=[{name, type A, ttl 300, rrdatas: ["198.51.100.7"]}], additions=[{name, type A, ttl 300, rrdatas: ["192.0.2.42"]}]`.
4. Provider returns the completed `Change`; daemon logs `reconcile_updated` with old/new and writes state.
5. Next tick at `t=N+poll_interval` observes `quorumIP == state.LastObservedIP == live.Rrdatas[0]` and logs `reconcile_noop`.

### 3.3 Resolver partial failure

1. Three sources configured, quorum 2. ipify returns `192.0.2.42`, ifconfig.me returns 502, icanhazip returns `192.0.2.42`.
2. Resolver returns `192.0.2.42` with quorum 2/3. Per-source report includes the 502 from ifconfig.me.
3. Tick proceeds normally; `reconcile_*` is logged as usual.

### 3.4 Resolver total disagreement (suspected poisoning)

1. Three sources, quorum 2. ipify returns `192.0.2.42`, ifconfig.me returns `203.0.113.9`, icanhazip returns `198.51.100.7`.
2. No IP reaches quorum. Resolver returns `ErrNoQuorum`.
3. Daemon logs `resolver_no_quorum` with all three source values, records `last_result=error` with message `"no quorum among echo services"`, and does not call the provider.
4. Next tick is attempted at the normal interval; no backoff because the provider was never involved.

### 3.5 Provider 429 rate limit

1. Provider `Upsert` returns a 429 from Cloud DNS.
2. Daemon logs `reconcile_error` with the provider error; increments backoff counter.
3. Next tick sleeps `min(poll_interval * 2^backoff, 30m)` before firing.
4. On eventual success, backoff resets to zero.

### 3.6 First run, record does not yet exist

1. Tick fires; resolver succeeds.
2. Provider `Get` returns `ErrNotFound` (404 from Cloud DNS).
3. Daemon constructs a `Changes.Create` with `additions=[…]` and no `deletions`, treating absent as an initial creation.
4. Provider succeeds; state records `last_result=updated`.

## 4. Configuration

Representative `config.yaml`:

```yaml
poll_interval: 5m
state_path: ~/.local/state/ddns
log_format: auto

resolver:
  sources:
    - https://api.ipify.org
    - https://ifconfig.me/ip
    - https://icanhazip.com
  quorum: 2
  timeout: 10s

records:
  - project: my-home-lab
    managed_zone: example-com
    name: home.example.com.
    type: A
    ttl: 300
```

Credentials are not in this file. ADC is found via the usual chain: `GOOGLE_APPLICATION_CREDENTIALS` env var → `gcloud` well-known file at `~/.config/gcloud/application_default_credentials.json` → GCE metadata server.

## 5. Error Taxonomy

```go
var (
    ErrNoQuorum     = errors.New("resolver: no quorum among echo services")
    ErrNotFound     = errors.New("provider: record not found")
    ErrTransient    = errors.New("provider: transient error, will retry")
    ErrConfig       = errors.New("config: validation failed")
    ErrAuth         = errors.New("provider: authentication failed")
)
```

Exit codes from the CLI:

| Code | Meaning |
|---|---|
| 0 | Success (including `noop` for `sync`). |
| 1 | Resolver could not reach quorum. |
| 2 | Provider error (network, 5xx, auth). |
| 3 | Config error, startup-time auth error, unrecoverable. |

## 6. Testing Strategy

- **Unit tests** per package: `config` validation cases, `resolver` quorum arithmetic against a fake HTTP server, `state` round-trip including crash-in-write simulation, `dnsprovider/gcp` against a recorded-cassette `httptest.Server` mirroring Cloud DNS response bodies.
- **Daemon tests** with `clock.FakeClock` and fake `IPResolver`/`DNSProvider` to exercise the full reconcile matrix (no drift → noop; state drift only → reconcile; record drift only → reconcile; both drift → reconcile; quorum failure → no call; provider 429 → backoff) without touching the network.
- **Integration test** (tagged `//go:build integration`) against a real Google Cloud project and a real managed zone, gated behind a `DDNS_INTEGRATION_PROJECT` env var so CI does not accidentally hit a live account.
- **Soak test** on the author's home network for 30 days prior to v1 tag, with state-file and log archives kept for post-hoc correctness review.

## 7. Observability

- Structured logs at every decision point (`startup`, `tick_start`, `ip_resolved`, `reconcile_noop`, `reconcile_updated`, `reconcile_error`, `resolver_no_quorum`, `shutdown`).
- Optional `/state` endpoint (P1) that dumps the current `State` JSON so a Prometheus blackbox or a simple `curl` is sufficient to confirm liveness without log parsing.
- Prometheus metrics (P1) listed in the PRD.

## 8. Deployment

One reference deployment shape is shipped under `deploy/`:

- `deploy/docker/Dockerfile` — `gcr.io/distroless/static-debian12:nonroot` base with the statically-linked binary, and a sample `docker-compose.yml` that bind-mounts a config file, the service-account JSON, and a named volume for state. Operators who want OS-level supervision wrap the container with their own init system; `ddns` itself does not ship unit files.
