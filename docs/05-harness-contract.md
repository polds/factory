# 05 — Harness Contract v1

The harness is the agent runtime: the thing that holds a conversation with a model, calls
tools, edits files, and runs commands inside the task sandbox.

Warp's worker shells out to `oz`, a proprietary CLI that authenticates to Warp
(evidence O6, U2). It is the only component of the self-hosted data plane that
cannot be reused. Replacing it is therefore the core engineering work of this project —
and the opportunity to make the harness a documented contract rather than a binary.

## The wedge

Three observations make a clean substitution possible (O5):

1. The worker mounts a sidecar image at `/agent` (`worker.go:696`).
2. It launches `/agent/entrypoint.sh` with assembled arguments (`docker.go:162`).
3. `coding_cli_sidecars` maps a harness name → an arbitrary image, mounted at
   `/mnt/{harness}-cli-sidecar` (`config.go:92-99`), explicitly documented upstream as
   "use this to bring your own Claude Code wrapper or custom harness binary".

So the harness is **already** a replaceable sidecar in the protocol we are
wire-compatible with. We supply our own `/agent` image and the worker neither knows nor
cares.

```
task container
├── /workspace            repo checkout(s), the agent's cwd
├── /agent/               ← harness sidecar (read-only mount)
│   ├── entrypoint.sh     the contract's entry point
│   ├── harness           adapter binary
│   └── adapters/
├── /mnt/claude-cli-sidecar/   optional extra tool sidecars
└── /var/run/factory/
    ├── task.json         the task spec (mode 0600)
    └── events.ndjson     fifo: harness → worker
```

## Invocation

The exact argv an unmodified worker produces, captured from a real run in
[`spike/m0`](../spike/m0):

```bash
/agent/entrypoint.sh agent run \
  --task-id run_m0_20261013T032059 \
  --sandboxed \
  --server-root-url http://127.0.0.1:8080 \
  --model m0-stub-model \
  --computer-use \
  --idle-on-complete ''
```

Three things the spike established that are easy to get wrong:

1. **`agent run` arrives as leading positional arguments**, before any flag — the worker
   invokes the agent CLI's subcommand path. A harness that assumes argv starts with a flag
   fails immediately. Accept and ignore unrecognized positionals.
2. **A flag may arrive with an empty value.** `--idle-on-complete ''` appears when neither
   the task nor the worker sets a timeout. Treat empty as absent, not as a parse error.
3. **The task container's working directory is `/workspace`** and the harness is mounted
   read-only at `/agent`.

The contract **accepts the observed `oz` flag vocabulary** (O6) so an unmodified
upstream worker can drive it:

| Flag | Meaning |
| --- | --- |
| `--task-id` | Logical run id; used for event correlation |
| `--model` | Model id; omitted for third-party harnesses, which carry their own |
| `--harness` | Adapter selector (`warp-agent`, `claude-code`, `codex`, `custom:<n>`) |
| `--profile`, `--skill`, `--mcp`, `--environment` | Agent configuration selectors |
| `--server-root-url` | Control plane root for transcript/usage reporting |
| `--session-sharing-server-url` | Live-attach channel |
| `--share public:VIEWER\|EDITOR` | Session sharing ACL |
| `--sandboxed`, `--bare` | Sandbox posture |
| `--computer-use`, `--computer-use-model`, `--no-computer-use` | Computer-use toggles |
| `--idle-on-complete <dur>` | Stay alive for follow-ups after the conversation ends |
| `--skip-initial-turn` | Attach without prompting (handoff/continuation) |
| `--repository-head-override-json` | Frozen checkout state (benchmarks — O7) |
| `--remove-repository-origins` | Strip remotes after clone |
| `--no-snapshot`, `--snapshot-upload-timeout`, `--snapshot-script-timeout` | Snapshot control |
| `--bedrock-inference-role`, `--bedrock-role-region` | AWS STS AssumeRoleWithWebIdentity |

Unknown flags **must be ignored with a warning**, not rejected. The worker forwards
`additional_oz_args` verbatim without deriving semantics (O3), so a newer control
plane plus an older harness must not deadlock on an unrecognized token.

Richer configuration arrives as JSON at `/var/run/factory/task.json` (the
`agent_config_snapshot` from the assignment — [04](04-protocols-and-apis.md)), because a
command line is a poor place for MCP server definitions and prompts.

## Output: NDJSON events

The harness writes one JSON object per line to `/var/run/factory/events.ndjson` (and
mirrors human-readable logs to stderr). The worker relays these to the control plane as
`run_event` messages in native mode, or collects them for terminal upload in v1 mode.

```jsonc
{"ts":"…","type":"turn.started","turn":1,"model_id":"claude-5-sonnet-high"}
{"ts":"…","type":"message.delta","turn":1,"role":"assistant","text":"Reading the test…"}
{"ts":"…","type":"tool.call","turn":1,"tool":"bash","redacted_input":"pnpm test -- link",
 "input_digest":"sha256:…"}
{"ts":"…","type":"tool.result","turn":1,"tool":"bash","ok":true,"duration_ms":41230,
 "output_digest":"sha256:…"}
{"ts":"…","type":"skill.read","skill_path":"skills/repository-conventions/SKILL.md"}
{"ts":"…","type":"file.changed","path":"docs/setup.md","additions":3,"deletions":1}
{"ts":"…","type":"usage","model_id":"…","input_tokens":18240,"output_tokens":1120,
 "cost_cents":31}
{"ts":"…","type":"question","gate":"clarification","text":"Which staging env?"}
{"ts":"…","type":"terminal","status":"succeeded","summary":"Opened PR #1182"}
```

Rules that matter:

- **Secrets never appear in `tool.call`.** The adapter redacts against the injected secret
  set before emitting, and emits a digest for correlation.
- **`usage` is per model call,** not per run, so router decisions can be attributed and
  `cost per PR` is computable by stage ([08](08-measure-and-self-improvement.md)).
- **`question` suspends the run** without consuming a worker slot once emitted with
  `--idle-on-complete` still pending.

## Exit codes

| Code | Meaning | Control-plane treatment |
| --- | --- | --- |
| `0` | Conversation completed | `task_completed` |
| `10` | Agent completed with findings/failure it reports itself | `task_completed`, run marked `failed_by_agent` |
| `20` | Configuration invalid (bad model, missing secret) | `task_failed`, `failure_reason=config_invalid`, no retry |
| `21` | Model provider unavailable | `task_failed`, `failure_reason=model_unavailable`, **retryable** |
| `30` | Sandbox/environment setup failed | `task_failed`, `failure_reason=environment_setup_failed` |
| `130`+ | Signalled (`128+signal`) | matches the worker's normalization (O3) |

Separating 10 from 20/21/30 is the point: only exit 10 is evidence about agent quality.
Everything else is infrastructure, and must not enter scoring.

## Adapters

| Adapter | Implementation |
| --- | --- |
| `claude-code` | Wraps the Claude Code CLI; maps NDJSON from its stream output; auth via `ANTHROPIC_API_KEY` or Bedrock/Vertex role |
| `codex` | Wraps the Codex CLI; `reasoningLevel` → effort flag; auth via `OPENAI_API_KEY` |
| `warp-agent` | Wraps Warp Agent CLI where an operator has a license; the one adapter we cannot ship an image for |
| `generic-openai` | Minimal built-in loop against any OpenAI-compatible endpoint, with a fixed tool set (bash, read, write, edit, grep, glob) — the escape hatch that keeps the system usable with local models |
| `custom:<name>` | Operator-built image honoring this contract |

`generic-openai` exists so the project has **no** required proprietary dependency. A
factory must be able to run end-to-end with a local model and no vendor account, or the
word "self-hostable" is doing a lot of work.

### Harness auth

Per-harness credentials are separate from the agent's execution secret allowlist
(evidence: `harness_auth_secrets` is its own wire field, and upstream documents harness
auth as "configured separately from the agent's secret allowlist"). We keep that
separation: a harness key is injected only into the harness process environment, is never
listed in the agent's `secrets`, and is never visible to tools the agent runs.

Sources:
- `managedSecret` + `secretName` — resolved by the secrets broker per run.
- `workerEnvironment` — read from the worker's own environment; only valid on a
  self-hosted pool, since it requires the operator to control the host.

## MCP servers

MCP definitions pass through as opaque JSON (the worker treats `mcp_servers` as
`map[string]json.RawMessage` — O3) and the harness is responsible for launching
them. Three transports, exactly one per entry:

- `command` + `args` + `env` — stdio child process.
- `url` + `headers` — HTTP/SSE.
- `managedId` — a deployment-managed installation with brokered OAuth.

Credentials in `env`/`headers` must be `{{SECRET_NAME}}` references, resolved by the
broker at injection time. The validator rejects anything that looks like inline
credential material, because an MCP header is an easy place to leak a token into Git.

## Computer use

`computer_use_enabled` + `computer_use_model_id` select a vision-capable model and add a
screen/input tool group. The adapter is responsible for providing a display (Xvfb +
a browser in the Linux reference image) and for emitting `screenshot` events whose
artifacts land in object storage. This is what makes "verify the UI actually changed"
expressible — and the visual evidence attached to a PR is the output.

## Conformance

A harness image is conformant if it passes `factoryctl harness verify <image>`, which runs
a fixture suite in a throwaway container:

1. Accepts every flag in the table above, ignoring unknown flags with a warning.
2. Emits well-formed NDJSON with monotonic timestamps and a single `terminal` event.
3. Redacts a planted secret from `tool.call` events.
4. Returns the correct exit code for each of: success, agent-reported failure, invalid
   model, unreachable provider, failing setup command.
5. Honors `--skip-initial-turn` and `--idle-on-complete` (process stays alive, accepts a
   follow-up, then exits).
6. Writes no files outside `/workspace` and `/var/run/factory`.

Conformance is a test suite rather than prose because "bring your own harness" is only
real if there is a way to prove a harness works before it runs on production work.
