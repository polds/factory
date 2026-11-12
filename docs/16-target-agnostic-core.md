# 16 — Target-agnostic core

Design. Baseline `main` `32fc0d1af2014bd913acd612b4e59e50fc7d5de7`. This page specifies the change. It does not implement it.

Factory is an open-source agent factory in the sense of Warp Factories: any repo, any codebase, custom agentic pipelines, several repos on one factory, several factories on one deployment, self-hosted on the operator's infrastructure with the Helm chart ([10](10-kubernetes-and-helm.md)). `factoryd` is a service that accepts definitions. Its code knows nothing about Dreaming or about any other repo it serves. Dreaming is one example factory (one definition, one repo) used to test and verify. Peter runs his own `factoryd` for Dreaming later. New Dreaming-specific code is frozen.

One `factoryd` process serves N factories. A factory is one applied definition. A factory lists one or more repositories. A run, a secret grant, a spend ceiling, a GitHub App token, and a publish target belong to one factory and to one repo in that factory's list. Repo, issue key, ref prefix, role-source path, model, and price are fields of a factory or of an invocation. They are not process-wide constants.

## Tenancy

The store already has the row shape this needs.

| Object | Where it is keyed | Baseline |
| --- | --- | --- |
| Factory | `factories.name` unique, `factories.id` | `internal/store/migrations/0001_init.sql:4-13` |
| Applied definition | `definitions.factory_id`, `git_sha`, status `applied` or `superseded` | `internal/store/migrations/0001_init.sql:16-24` |
| Run | `runs.factory_id` | `internal/store/migrations/0001_init.sql:57-59` |
| Invocation | `invocations.factory_id`, `definition_revision`, `definition_digest` | `internal/store/migrations/0013_m8_invocations.sql:3-11` |
| Spend lines | `factory:<factoryID>:<invocationID>` plus invocation, lead, and agent lines. That factory line includes the invocation id, so it dies with the invocation. | `internal/store/native_runtime.go:323-334`. `budget_scopes.invocation_id` is `NOT NULL` and `ON DELETE CASCADE` (`internal/store/migrations/0015_native_runtime.sql:137-140`). |

What is still a process singleton, and what replaces it:

| Singleton | Baseline | Replacement |
| --- | --- | --- |
| One git definition source | `DefinitionPath`, `DefinitionGitURL`, `DefinitionGitRef`, `DefinitionGitSubdir`, `DefinitionGitProductionRef` (`internal/server/server.go:37`, `internal/server/server.go:65-74`) | Stays a deployment singleton through TA: one bootstrap git factory. `PUT /api/v1/factory/{name}/definition` is already per factory. TF-2 stores a source record per factory and runs one poller per git-backed factory. The process env is the default copied onto a factory that omits a source. |
| One secret map | `Config.Secrets`, filled from `FACTORY_SECRET_*` (`internal/server/server.go:52`, `cmd/factoryd/config.go:213-225`) and passed whole into each resolver (`internal/server/native_assignment.go:79-91`) | TA-3. `<factory>` is the factory id, not the name. The env key is `FACTORY_SECRET_<factoryID>__<NAME>`. The chart key is `<factoryID>.<NAME>` because a Kubernetes Secret key cannot contain `/`. The bootstrap factory, whose id does not exist until first boot, uses chart key `bootstrap.<NAME>` and env `FACTORY_BOOTSTRAP_SECRET_<NAME>`; factoryd binds those values onto the created factory id and does not fall through to an unprefixed name. `NAME` is `[A-Z][A-Z0-9_]*` and contains no `__`. There is no fall-through to `FACTORY_SECRET_<NAME>`. `FACTORY_SECRET_ALLOWLIST` and `FACTORY_SECRET_MOUNT_DIR` stay configuration (`internal/server/config.go:86-88`). The parser skips those two suffixes before it splits on `__` (`cmd/factoryd/config.go:249-251`). A missing prefixed value fails closed. An existing install's unprefixed `FACTORY_SECRET_<NAME>` stops resolving at TA-3. The operator copies each value to the prefixed key before that deploy. |
| One spend-price document | `Config.SpendPrice`, else `FACTORY_SPEND_PRICE` (`internal/server/server.go:48-51`, `internal/server/run_spend.go:32-37`) | `inference.rates` on the factory, copied onto the invocation at open. |
| One publish repo | `publishRepo` and `FACTORY_PUBLISH_DRAFT_REPO` (`internal/server/publish_draft.go:18-21`, `internal/server/publish_draft.go:92-95`) | `publish` on the factory. A run may narrow issue key, title, body, and ref prefix. It may not name a repo outside the factory. |
| One role checkout | `FACTORY_ROLE_SOURCE` (`internal/server/produced_bundle.go:468-472`) | A cache entry `(factory_id, source_commit)` filled from the factory's role repo. |
| One GitHub App | `Config.GitHubAppID`, `Config.GitHubAppKey` (`internal/server/server.go:82-83`) | Stays a deployment credential. Every minted token names only repos that factory lists. |
| One inference gateway | `Config.InferenceBaseURL`, set from `FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL` (`internal/server/server.go:41`, `cmd/factoryd/config.go:115`). Assignment copies it to `OPENAI_BASE_URL` when ambient inference is on (`internal/server/assign.go:90-91`). The companion `InferenceAPIKey` is the same deployment secret (`cmd/factoryd/config.go:116`). | Stays a deployment singleton. Every factory on the process uses this URL. `omitAmbientInference` skips it for a scanner, native, or claude-code harness, and for a read-only forge target (`internal/server/native_assignment.go:103-105`, `internal/server/native_assignment.go:130-138`). |
| One admin API key | `FACTORY_API_KEY` (`cmd/factoryd/config.go:109`). `withAPIKey` compares the bearer to that one value (`internal/api/handler.go:142-158`). | Stays a deployment-admin singleton. It can read every factory. It is not the actor in an isolation check. TA-3 adds factory-scoped keys. |
| One worker API key | `FACTORY_WORKER_API_KEY` (`cmd/factoryd/config.go:110`) | Stays a deployment singleton. Workers are a shared pool. A task env still receives only that run's token and that factory's secret grants. |
| One evidence admin URL | `FACTORY_DISPOSABLE_ADMIN_URL`, else `Config.DatabaseURL` (`internal/server/disposable.go:134-138`) | Stays a deployment singleton. Evidence databases share that cluster. Isolation is the per-run database and role, not a second server. The admin URL is not copied into a task env. A per-factory admin URL is out of scope. |
| One ingress app | `FACTORY_GITHUB_WEBHOOK_SECRET`, `FACTORY_SLACK_*`, `FACTORY_LINEAR_*` (`cmd/factoryd/config.go:139-148`) | Stay deployment singletons. One webhook URL, one Slack bot, one Linear key. After the signature check, a delivery routes to the one factory that lists the repo or the external team. When more than one factory lists that repo, the delivery fails closed and starts no run. Per-factory Slack or Linear apps are out of scope, same as a second GitHub App key. |
| GitHub token mode | `FACTORY_GITHUB_TOKEN_MODE` (`cmd/factoryd/config.go:59`, `cmd/factoryd/config.go:140`) | `app` is required once a second factory is registered. `pat` stays legal for a single-factory process, which is today's default. Apply of a second factory fails closed while mode is `pat`, so a planted `GH_TOKEN` is not a cross-repo credential. |

Per-factory gateways are a later option. The definition schema already accepts `inference.providers[].baseUrl` (`internal/definition/types.go:357`, `docs/03-definition-schema.md:122`). Assignment does not read that field. TA-1 through TA-5 do not route model calls per factory. A factory that needs a different gateway runs in its own deployment, with its own `InferenceBaseURL`. Rate documents stay per factory either way.

A factory-scoped API key, and a run token, cannot read another factory's runs, role bytes, secret values, or publication rows, and cannot charge another factory's ceiling. The deployment admin key can. Isolation checks use the factory-scoped key and the run token, not `FACTORY_API_KEY`.

A scoped key may act only on its own factory. The check is in `internal/api/handler.go`, on the API-key wrapper TA-3 adds beside `withAPIKey` (`internal/api/handler.go:142-158`), before the route handler runs. `POST /api/v1/agent/run` takes the factory from the JSON body (`internal/api/handler.go:298-312`). `POST /api/v1/agent/schedules` takes `factory_id` from the body (`internal/api/types.go:304`). When that value is not the key's factory, the handler returns 403 before any insert or reserve. The same check covers every other write whose body names a factory.

List and query routes are a fourth shape. `GET /api/v1/agent/runs` filters only when `?factory_id=` is set (`internal/api/handler.go:253-265`). `GET /api/v1/agent/schedules` returns every schedule when `factory` is empty (`internal/api/integrations.go:249-254`). `GET /api/v1/factory` lists every factory (`internal/api/handler.go:177-184`). `GET /api/v1/agent/run-by-external-reference` (`internal/api/handler.go:106`) resolves across factories. For a scoped key the same wrapper forces the filter to the key's `factory_id` before the query. A `factory_id` or `factory` query that names another factory is 403. `GET /api/v1/factory` returns only the key's factory. An external-reference hit on another factory's run is an empty 404. The deployment admin key keeps the unfiltered lists.

`PUT /api/v1/factory/{name}/definition` and `POST .../definition:apply` are deployment-admin only. A scoped key receives 403 on both, including for its own factory. `:validate` is allowed for a scoped key when the path uid is that key's factory, and it writes nothing.

Two factories may list the same repo only when the admin applies both definitions. A scoped key cannot add a repository, so it cannot point its role source at another factory's repo. Role bytes are read only for the factory whose definition the admin applied, into that factory's cache. Ingress still fails closed when more than one factory lists the repo, because a webhook cannot choose a factory. Spend and secret values stay on the calling factory.

The deployment is one trust domain: one operator, one database, one Helm release. Isolation inside that domain is `factory_id`. Operators who also want Kubernetes RBAC between teams put Factory resources in different namespaces. `factoryd` still enforces `factory_id` when they share a process.

## Where a definition lives

A definition is registered with `factoryd`. It is not implied by the repo a run edits.

Each factory has one source, fixed at create:

| Source | How it is loaded | Version pin |
| --- | --- | --- |
| `git` | URL, ref, subdir, production ref. Through TA this is the one process git source. TF-2 stores those fields per factory and runs the poller (`internal/defsync/doc.go`) once per git-backed factory. A non-production ref, a validation error, or an unchanged commit leaves the last applied tree. | `definitions.git_sha` |
| `api` | `PUT /api/v1/factory/{name}/definition` with `generation` and `files` ([04](04-protocols-and-apis.md#factories-and-definitions)). A git-backed factory returns 409 and writes nothing. | published generation, stored as the definition row |
| `path` | Local directory. Dev and the chart bootstrap factory only. | the apply digest |

Create rejects a second source on an existing factory. The current 409 for "git-backed" stays.

An invocation stores `definition_revision` and `definition_digest` (`internal/store/migrations/0013_m8_invocations.sql:10-11`). Completion, attestation replay, and publish read that pin. A later apply does not retarget an in-flight run.

A factory may set `definition.repo` to one repository it already lists, plus a subdir (default `.factory/`). That is still a registration: the factory row records the remote, and apply validates the tree before it serves. The work tree a seat edits is a different pin (`pinned_head_sha`, and the role `sourceCommit`). The publish ref prefix cannot name the definition ref. The publisher's token is not granted a push to the definition branch.

Role bytes are a third pin. They are attested at open and frozen on the policy. They are not the definition git sha, and they are not written into the Factory git tree. The example definition records paths and blob ids. It does not record file bodies.

Helm chart-only mode keeps one bootstrap factory from `controlPlane.definition` (`deploy/helm/factory/values.yaml:70-77`). Further factories are registered with a git URL, an API put, or a Factory CRD ([10](10-kubernetes-and-helm.md#factory)). The chart does not grow a list of target repos.

## Security properties that stay

These stay generic mechanisms. TA does not weaken them.

| Property | Mechanism that stays |
| --- | --- |
| Exact-SHA reviewer gate before publish | The declared reviewer and the declared implementer both have a ready produced head, and the SHAs are equal (`internal/server/publish_draft.go:438-454`). The publisher checks out that SHA, requires a clean porcelain and `HEAD` equal to it (`internal/server/publish_draft.go:633-652`), and verifies the bundle tip before push. |
| Role-source attestation by git blob | Byte-for-byte compare of the roster and each seat file against a local commit. SHA-256 and git blob id both mismatch-fail. Absent object fails. The compare does not fetch and does not write the bytes into the Factory checkout (`internal/definition/attest.go:17-19`, `internal/definition/attest.go:33-56`). |
| Spend hard stop | Integer cents. Ancestor lines lock in `scope_id` order. A reserve past the ceiling is `budget_denied` and pauses the root so publish refuses (`internal/server/run_spend.go:100-124`). `PublicationSpendBlocked` refuses the draft (`internal/server/publish_draft.go:355-365`). Worst-case `W` comes from the frozen price document (`internal/harness/spend.go:36-72`). |
| Draft-only publish | The pull request is created with `draft: true`. A non-draft response or a head mismatch fails the row (`internal/server/publish_draft.go:274-288`). The publisher has no merge, ready, label, reviewer, or comment call. |
| No token to seats | The installation token is minted for the publish repo only, after the reviewer gate, with `contents: write`, `pull_requests: write`, `metadata: read` (`internal/server/publish_draft.go:315-319`). It is not placed in a task environment, event, or log. Log text is scrubbed (`internal/server/publish_draft.go:771-786`). |
| `reportsTo` grants nothing | `ReportsDoNotGrantSecrets` returns the secret list unchanged (`internal/definition/native_tree.go:153-160`). |
| One draft per issue | `draft_publications` stays unique on `(target_repo, issue_key)` (`internal/store/migrations/0019_draft_publication.sql:21-22`). A second invocation does not mint a second token. |

The publication row is still inserted as `pending` before the token is minted. Root cancelled, paused, or not completed still refuses (`internal/server/publish_draft.go:369-397`). A truncated evidence transcript, a missing named red or green test, a dirty tree, and a missing sentinel still refuse.

## Coupling inventory

Grep baseline: `dreaming`, `northstar`, `oskar`, `viktor`, `nils`, `beatriz`, `roster`, `SOUL`, `HEARTBEAT`, `proof`, plus the env names below. Test fixtures and `testdata/` may keep target names. Non-test `cmd/` has none of these identifiers at this baseline. Non-test hits under `internal/` are the rows below.

### Publish target

`internal/server/publish_draft.go:19-22` fixes the repo `northstar-ai-labs/dreaming`, the issue key `dreaming#454`, the ref prefix `factory/dreaming-454-`, and the title `Factory: DB-backed tenant-isolation tests (#454)`. `FACTORY_PUBLISH_DRAFT_REPO` only gates entry (`internal/server/publish_draft.go:92-95`) and is then required to equal that constant (`internal/server/publish_draft.go:400-402`). The pending row stores the constants (`internal/server/publish_draft.go:146-151`). Remote, pull listing, create, and installation lookup all use the constant (`internal/server/publish_draft.go:174`, `internal/server/publish_draft.go:210`, `internal/server/publish_draft.go:237`, `internal/server/publish_draft.go:270`, `internal/server/publish_draft.go:311`). The body is fixed prose, including `Oskar evidence` and `Refs #454` (`internal/server/publish_draft.go:748-756`). The ref suffix is the invocation id's first 8 characters (`internal/server/publish_draft.go:730-745`).

Replacement is the `publish` object below. Defaults: `enabled: false`. With `enabled: false`, or with no `publish` object, the root completion returns without a publication row, which is today's empty-env behavior. `enabled: true` with an empty repo, an empty issue key, a repo the factory does not list, a ref prefix that fails the grammar, or a body template that fails to render fails closed before a token mint. The request may set `issueKey`, `title`, `body`, and `refPrefix`. The repo comes from the definition only. The ref grammar is `^factory/[A-Za-z0-9][A-Za-z0-9._-]{0,48}-$`. The default prefix is `factory/draft-`. The suffix check at `internal/server/publish_draft.go:730-745` stays. Title cap 256 bytes. Body template cap 8192 bytes. The template data is only `Head`, `Pin`, `Red`, `Green`, `Counted`, `Sentinel`, `IssueKey`, `Repo`. An unknown or empty field fails the render. The default body is:

```text
Head {{.Head}}
Pin {{.Pin}}
Evidence: red {{.Red}} failed; green {{.Green}} passed.
Spend counted cents: {{.Counted}}
Sentinel: {{.Sentinel}}
Issue: {{.IssueKey}}
Repo: {{.Repo}}
Draft by Factory; human review required; Factory will not merge.
```

`draft_publications` gains `factory_id`, matching the invocation's factory. Uniqueness on `(target_repo, issue_key)` stays global, so two factories that both list one repo cannot open two drafts for one issue key. A unique violation already refuses with `reasonIssue` and does not read the other row (`internal/server/publish_draft.go:153-155`). That stays. Once the row carries `factory_id`, every lookup of an existing publication filters by the calling factory. The response still carries no pull number, URL, ref, SHA, or detail from another factory's row. Same-invocation replay still returns that invocation's own row (`internal/store/draft_publication.go:48-50`). The token repository list is that one repo.

### Role source

`AttestRoleSource` loads `org/roster.yaml` and `org/agents/<name>/<file>` (`internal/definition/attest.go:33-46`). `rolePath` allows only that roster path and the four names `AGENTS.md`, `HEARTBEAT.md`, `SOUL.md`, `TOOLS.md` (`internal/definition/attest.go:90-117`). `LoadPinnedRoles` uses the same roster constant and the same four names (`internal/definition/pinned_roles.go:19`, `internal/definition/pinned_roles.go:27-28`) and reads `org/agents/<slug>/<name>` (`internal/definition/pinned_roles.go:385-386`). The open path takes the checkout from `FACTORY_ROLE_SOURCE` (`internal/server/produced_bundle.go:468-472`). `NativeTreeRequest` carries personas, depth, and roster hashes, and carries no path template (`internal/api/native_runtime.go:28-48`, `internal/client/native_runtime.go:29-49`).

The declaration is native in `factory.yaml`. The load path reads that declaration. An importer may emit the declaration for a repo whose layout is already Dreaming's. The importer lives at `examples/dreaming-definition/import-roles/`. It is an authoring tool: it writes paths and blob ids, it does not copy role bytes into the output, and `factoryd` does not call it. `cmd/` stays free of layout names so the denylist stays empty there. A load-path importer would put one repo's layout back on the open path.

`roleSource` fields:

| Field | Default | Validation | Fail closed |
| --- | --- | --- | --- |
| `repo` | none | Must be one of `repositories` | Role-files open with no `roleSource` is rejected. Legacy trees with no pins and no seat files skip attestation, as today (`internal/server/produced_bundle.go:475-484`). |
| `roster.path` | none | Relative, `filepath.Clean` stable, no `..`, not absolute | Absent object or a symlink, as `readPinned` does today (`internal/definition/pinned_roles.go:504-536`). |
| `seats.pathTemplate` | none | Contains `{slug}` once and `{file}` once. No other braces. | Substitution that fails the same lexical rules as `rolePath` is an absent object. |
| `seats.files` | none | 1 to 16 single path segments, unique, order is prompt order and digest order | A seat with a different set or order fails before queue (`internal/definition/pinned_roles.go:375-383`). |

Roster document, generic, required keys only: a mapping with an `agents` sequence. Each agent has `slug`, `role`, optional `reports_to`. Unknown keys fail. The current required `meta` mapping (`internal/definition/pinned_roles.go:453-478`) is dropped. `role` matches `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`. The fixed set `ceo`, `cto`, `manager`, `general`, `worker` (`internal/definition/pinned_roles.go:30-32`) is dropped. Caps stay: roster 262144 bytes, each role file 65536 bytes (`internal/definition/pinned_roles.go:17-18`).

Attestation still uses local `git cat-file` and still compares SHA-256 and git blob id. The server fills a cache directory for `(factory_id, commit)` from the role repo with a `contents: read`, `metadata: read` installation token on that repo only, then attests against the cache. The compare itself still does not fetch. A commit that is not a 40-hex commit fails before insert, as today (`internal/server/produced_bundle.go:465-466`). The cache is not an artifact path and is not committed.

The path template is stored on the policy outside `treeDigest`. `treeDigest`'s JSON shape stays (`internal/definition/native_tree.go:218-249`), so an already frozen policy digest still matches.

### Hierarchy and proof shape

`NativeProofMaxDepth` is 2 and `ValidateNativeTree` requires `MaxDepth` to equal it and the deepest persona to reach it (`internal/definition/native_tree.go:13-17`, `internal/definition/native_tree.go:99-101`, `internal/definition/native_tree.go:116-118`). The same function requires `Synthetic` and rejects `AtumRoster` (`internal/definition/native_tree.go:90-92`). Reload forces `Synthetic: true` so the reloaded policy still passes that gate (`internal/store/native_runtime.go:288-291`). `LoadPinnedRoles` requires `ProofRoot` to be the single seat whose manager lies outside the selection (`internal/definition/pinned_roles.go:52`, `internal/definition/pinned_roles.go:79-81`, `internal/definition/pinned_roles.go:346-361`) and rejects a parent whose source role is `general` or `worker` (`internal/definition/pinned_roles.go:366-369`). It also stamps the proof depth onto the tree (`internal/definition/pinned_roles.go:110-117`). `seatNames` then finds the implementer as the only depth-2 persona and the reviewer as the only depth-1 leaf (`internal/server/produced_bundle.go:411-447`).

Replacement:

| Rule | Behavior |
| --- | --- |
| One root, acyclic, connected | Stays. Root count other than 1 fails (`internal/definition/native_tree.go:183-185`). |
| `maxDepth` | Definition field. Default 8. A persona deeper than `maxDepth` fails validation. The exact-equality check and the "must reach depth 2" check are removed. |
| `absoluteMaxDepth` | Definition field. Default 8. Global maximum 8, the current `NativeAbsoluteMaxDepth`. A value outside 1..8 fails validation. `CheckDepth` still refuses a task chain past either frozen number (`internal/definition/native_tree.go:142-150`). |
| Request depth | A `NativeTreeRequest` may set a lower `maxDepth` or `absoluteMaxDepth`. A value above the definition, or an `absoluteMaxDepth` above 8, fails the open before insert. |
| `Synthetic`, `AtumRoster` | Stop being gates. `Synthetic` stays in the digest JSON (`internal/definition/native_tree.go:232`). It is not a column today. Reload calls `ValidateNativeTree` and does not recompute the digest (`internal/store/native_runtime.go:204`). A row opened before TA-2 has no stored flag. Reload of that row still supplies `Synthetic: true`, which is the value its digest was computed with (`internal/store/native_runtime.go:288-291`). A row opened after TA-2 persists the flag on the policy envelope, and reload uses that stored value. |
| Declared root | `native.root` is the one seat whose frozen `reportsTo` is empty. Its source `reports_to` is kept as `sourceReportsTo` and is not a delegation edge. Every other selected seat's manager must be in the selection. Two seats with a manager outside the selection fail as two roots. |
| `dotted_to` | Still rejected (`internal/definition/pinned_roles.go:351-353`). |
| Parent source role | No longer a gate. |
| Delegation | `direct_manager` stays required (`internal/definition/native_tree.go:96-98`). `allowAncestorShortcut: true` fails validation. The field stays in the digest. Turning the shortcut on is a later contract change. |
| Implementer and reviewer | `native.implementer` and `native.reviewer` are slugs in the selection. Publish and reviewer evidence use those names. Depth is not how a seat is classified. When either slug is empty, publish fails with the existing ready refusal and the reviewer DSN is not attached. |
| Persona cap | 256 personas in one tree. Over that, validation fails. |
| Seat file count | The declared file list, 1..16. |

`reportsTo` still grants no secrets and no repository access.

### Evidence, sentinel, and the test DSN

The package and the mutation path come from the process environment (`internal/server/disposable.go:30-32`, `internal/server/disposable.go:47-56`, `internal/server/disposable.go:85-91`). The publisher reads the same mutation env and requires the blob at the reviewed SHA to contain `factoryEvidencePass` (`internal/server/publish_draft.go:654-662`). The harness rewrites that constant to `factoryEvidenceFail` (`internal/harness/mutate.go:11-14`, `internal/harness/mutate.go:71-79`). The disposable database URL is `DREAMD_TEST_DSN` (`internal/harness/materialize.go:35-37`).

Replacement is the `evidence` object on the definition, frozen on the invocation. The request may set `package` and `mutation`. `sentinel` and `break` come from the definition only, so a run cannot point the publisher at a different token than the harness rewrites.

| Field | Default | Validation |
| --- | --- | --- |
| `package` | empty | `EvidencePackagePath` (`internal/harness/evidence.go:32-36`). Empty with publish enabled fails closed. |
| `mutation` | empty | `EvidenceMutationPath` (`internal/harness/evidence.go:39-44`). Empty with publish enabled fails closed. |
| `sentinel` | `factoryEvidencePass` | 1..64 bytes, no newline. The reviewed blob must contain it. |
| `break` | `factoryEvidenceFail` | Same grammar, different from `sentinel`. |

The prompt still does not choose the package or the path. Lexical `..`, absolute paths, and symlinks still fail. The DSN env name becomes `FACTORY_EVIDENCE_DSN`. Only the declared reviewer receives it, and only when the implementer's produced SHA is ready and both package and mutation are set. Any other assignee does not. The admin URL is still not copied into the task environment.

`factoryEvidencePass` stays a generic marker. It is not a target name and it is not on the denylist.

The evidence database and role are named `dreamd_` plus 32 hex (`internal/store/disposable.go:21`, `internal/server/disposable.go:503-507`, `internal/server/disposable.go:568-573`). That prefix is Dreaming's daemon name. TA-3 creates `factory_ev_` plus 32 hex only. The steady-state drop sweep accepts that shape only. A one-shot migration in `internal/store/migrations/` drops databases and roles already stored in `disposable_databases` whose names match `^dreamd_[0-9a-f]{32}$`, then deletes those ledger rows. It drops no other name. The denylist token is `dreamd`. The scanner skips `internal/store/migrations/*.sql`, so that one-shot file may name the old prefix. Create, `storeName`, and the sweep in Go may not.

### Spend price and model

Issue [#217](https://github.com/polds/factory/issues/217) tracks the live price document: Dreaming's driver still builds `workedPrice()` (`deploy/dreaming-live/offline.go:234-242`) and hands it to the server. `factoryd` already computes `W` from a price document rather than from a Dreaming constant (`internal/server/run_spend.go:32-37`, `internal/harness/spend.go:69-72`). TA treats that split as the landed shape and does not call a gateway.

The document moves to `inference.rates` on the factory (the same JSON as `SpendPrice`). Apply copies it onto the factory row. Open copies it onto the invocation. Reserve and the harness read the invocation copy. A chart default, when the definition omits rates, is copied onto the factory at apply. A later chart change does not change an in-flight invocation. A `role_files` open with an empty or unparsable frozen document fails before queue. Legacy runs that are not `role_files` keep today's behavior: no spend URL is attached (`internal/server/run_spend.go:39-46`).

The model id stays the agent `model` / `harness` field ([03](03-definition-schema.md#factoryyaml)). The Dreaming model string belongs in the example definition. Two factories on one process can name different models and different rate documents.

The hard stop remains a ceiling. Today's ceiling is the invocation's. TA adds a factory cap and an optional per-repo cap that outlive one invocation, because the existing scope row cannot: `invocation_id` cascades away (`internal/store/migrations/0015_native_runtime.sql:137-140`).

| Cap | Field | Scope id | Rule |
| --- | --- | --- | --- |
| Factory | `budget.ceilingCents` | row in new table `factory_budgets`, keyed by `factory_id` | Required when the factory uses `role_files` or `publish.enabled`. No implicit default. A `role_files` open without it fails before queue. Legacy factories that omit it keep invocation ceilings only. |
| Repo | `repositories[].ceilingCents` | row in `factory_budgets`, keyed by `factory_id` plus `owner/name` | Optional. When set, a run whose target repo is that repo counts against this cap and the factory cap. |
| Invocation | existing `ceilingCents` on the open | `internal/store/native_runtime.go:323-334` | May be lower than the factory cap and the repo cap. A request above either fails the open. |

Reserve locks the invocation lines and the `factory_budgets` rows in id order, in the same transaction as today. A reserve that would pass any locked ceiling is `budget_denied` and pauses the root (`internal/server/run_spend.go:100-124`). Deleting an invocation does not delete `factory_budgets`. One factory's charges do not change another factory's spent or reserved cents.

#217 may later fill `inference.rates` from gateway rates. TA does not wait on that issue, and TA does not add a gateway client.

### Other target-shaped surfaces

| Surface | Baseline | TA treatment |
| --- | --- | --- |
| Dreaming harness | `deploy/dreaming-live`, 6145 lines of Go, package `dreaminglive` | Becomes the conformance suite in the last PR. Deleted there. |
| Spike driver | `spike/dreaming/*`, including `spike/dreaming/definition/seats.yaml:1-18` (`proof_root: nils-aaberg`, `publish_repo: northstar-ai-labs/dreaming`) | Deleted in the last PR. The example definition replaces `seats.yaml`. |
| Live workflow | `.github/workflows/dreaming-live.yml:14-44` (`dreaming-live-454`, concurrency `dreaming-live`) and `:138-139` (repo and `FACTORY_ROLE_SOURCE`) | Replaced by a workflow that takes a definition path. Not dispatched by this work. |
| CI step | `.github/workflows/ci.yml:275-276`, `MODE=offline ./spike/dreaming/run_test.sh` | Renamed in the last PR. Until then the command stays green. |
| Docs that describe the constant | Role files at `docs/14-native-coordination.md:7`. Publish when the env repo is Dreaming at `docs/14-native-coordination.md:15`. | Updated in the PR that changes the behavior they describe. |
| Test fixtures | `internal/server/publish_draft_test.go`, `internal/definition/pinned_roles_test.go`, and the rest of `*_test.go` | May keep Dreaming names. The guard does not scan them. |

`draft_publications` has no token column and no bundle bytes (`internal/store/migrations/0019_draft_publication.sql:1-3`). That stays.

## Schema

Added to `factory.yaml` ([03](03-definition-schema.md#factoryyaml)). Unknown keys are already errors (`internal/definition/validate.go:15`). TA-1 registers these keys in the loader, in `schemas/v1alpha1/factory.schema.json`, and in the Factory CRD. `docs/13-defined-factory-contracts.md:53` replaces the hierarchy row "proof depth 2, absolute max 8" with "maxDepth and absoluteMaxDepth from the definition, each at most 8". Contract version stays `defined-factory/1`. The digest JSON shape stays.

```yaml
definition:                         # optional. Registration, not the work tree.
  repo: { codeForge: GITHUB, owner: acme, name: factory-defs }
  subdir: .factory

roleSource:                         # required for bundleMode role_files
  repo: { codeForge: GITHUB, owner: acme, name: payments-service }
  roster: { path: team/roster.yaml }
  seats:
    pathTemplate: "team/roles/{slug}/{file}"
    files: [ROLE.md]

native:
  limits:
    maxDepth: 8                     # 1..8, default 8
    absoluteMaxDepth: 8             # 1..8, default 8, global max 8
  root: lead
  implementer: worker               # required when publish.enabled or evidence is on
  reviewer: reviewer
  seats: [lead, worker, reviewer]

publish:
  enabled: false
  repo: { codeForge: GITHUB, owner: acme, name: payments-service }
  issueKey: payments#120
  refPrefix: factory/draft-
  title: "Factory draft"
  body: ""                          # empty selects the default template

evidence:
  package: ./oracle
  mutation: oracle/oracle.go
  sentinel: factoryEvidencePass
  break: factoryEvidenceFail

budget:
  ceilingCents: 2500                # factory-wide hard stop; required for role_files

inference:
  rates:                            # SpendPrice JSON, snapshotted per factory
    prompt_byte_cap: 400000
    max_tokens: 8192
    models:
      - name: example-model
        input_cost_per_token: "0.000003"
        output_cost_per_token: "0.000015"
```

`repositories` is the allowlist. `roleSource.repo` and `publish.repo` must appear in it. A deferred repo may be a publish target only when it is listed.

`NativeTreeRequest` gains optional `publish` (`issueKey`, `title`, `body`, `refPrefix`) and `evidence` (`package`, `mutation`). It does not gain a repo, a role path, a sentinel, or a price. Those come from the pinned definition. The existing persona, depth, and roster-hash fields stay (`internal/api/native_runtime.go:28-48`).

Dreaming, as data under `examples/dreaming-definition/` and nowhere under `internal/` or `cmd/`:

```yaml
name: dreaming
repositories:
  - { codeForge: GITHUB, owner: northstar-ai-labs, name: dreaming }
roleSource:
  repo: { codeForge: GITHUB, owner: northstar-ai-labs, name: dreaming }
  roster: { path: org/roster.yaml }
  seats:
    pathTemplate: "org/agents/{slug}/{file}"
    files: [AGENTS.md, HEARTBEAT.md, SOUL.md, TOOLS.md]
native:
  limits: { maxDepth: 2, absoluteMaxDepth: 8 }
  root: nils-aaberg
  implementer: viktor-halas
  reviewer: oskar-lindgren
  seats: [nils-aaberg, beatriz-salgado, viktor-halas, oskar-lindgren]
budget:
  ceilingCents: 2500
publish:
  enabled: true
  repo: { codeForge: GITHUB, owner: northstar-ai-labs, name: dreaming }
  issueKey: dreaming#454
  refPrefix: factory/dreaming-454-
```

The second fixture is `examples/widget-factory/`. It is offline only. One repo `example/widget`. Role paths `team/roster.yaml` and `team/roles/{slug}/ROLE.md`. Seats `lead`, `worker`, `reviewer`. Issue key `widget#1`. Ref prefix `factory/draft-`. `budget.ceilingCents` is a different integer from the Dreaming example. Its rate document is its own. The conformance process loads both factories. `example/widget` is not a live target.

TF-1's second definition is an input file, not this fixture. The operator supplies it at dispatch: a real repository where the GitHub App is installed, role files at a pinned commit, and an issue key. The workflow reads the repo, the ref, and the issue key from that file. Peter provides the file when he authorizes the run. It is not committed when it would contain private role bytes.

## Helm and config

| Setting | Where it lives | Why |
| --- | --- | --- |
| Image, replicas, database, object store, ingress, OIDC, worker pools | Chart values ([10](10-kubernetes-and-helm.md#values)) | Deployment machinery. Shared by every factory on the release. |
| GitHub App id and private key | Chart secret, one per deployment | Operator credential used to mint installation tokens. Not an agent secret. Not a repo name. |
| Bootstrap definition | `controlPlane.definition` | One smoke factory so `helm install` still serves a tree. |
| Default `inference.rates` | Optional chart value | Copied onto a factory at apply when that factory omits rates. Not read live at reserve time. |
| `InferenceBaseURL` | Chart env or process env `FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL` | One gateway for the deployment. Not a per-factory field in TA. |
| `workers[].securityProfile` | Chart worker entry | Replaces `workers[].proof` (`deploy/helm/factory/values.yaml:233`, [PR #199](https://github.com/polds/factory/pull/199)). `locked` is today's proof behavior. The chart does not gain a target repo. |
| Provider API keys | Chart secrets | Deployment credentials. Injected for a factory only through that factory's grants. |
| Repositories, role paths, hierarchy limits, publish, evidence, models, secret names | Factory definition | Per factory. Several factories per release. |
| Secret values | `FACTORY_SECRET_<factoryID>__<NAME>` and chart key `<factoryID>.<NAME>`. Bootstrap, before the id exists, uses chart key `bootstrap.<NAME>`. | The singleton row is the only definition of this namespace. |
| `FACTORY_API_KEY` | Chart secret, one per deployment | Deployment admin. Not a factory credential. |
| Factory-scoped API keys | Per factory, created at apply | The isolation actor for REST reads. |
| `FACTORY_WORKER_API_KEY` | Chart secret, one per deployment | Shared worker pool. |
| `FACTORY_DISPOSABLE_ADMIN_URL` | Chart env | One evidence cluster for the deployment. |
| Slack, Linear, GitHub webhook secrets | Chart secrets, one set per deployment | One ingress endpoint. Routing is after verify. |
| Denylist | `scripts/target-denylist.txt` in this repo | CI for Factory's own tree. Not a chart value. |

`values.yaml` does not gain a target repo, an issue key, a role path, or a persona list. `global.image.registry` stays `ghcr.io/acme` (`deploy/helm/factory/values.yaml:5`) through TA-5.

`workers[].proof: true` is the locked worker from PR #199. The template fails the render when that flag is set and `setupCommand` or `teardownCommand` is set, and it omits both keys otherwise (`deploy/helm/factory/templates/_worker.tpl:125-127`, `deploy/helm/factory/templates/_worker.tpl:147-154`). `docs/06-execution-backends.md:252` records the same rule. TA-5 generalizes the flag into `securityProfile`. The only non-default value in TA is `locked`, with that same render rule. An empty profile, or `standard`, still renders a configured command. The example worker's name, pool, and worker id change from `proof` to `locked` so the chart example is not a proof pool. Golden files and `deploy/helm/factory/tests/proof-worker.sh` update with the render. That slice does not publish images, change the registry, or add a replica.

## Conformance suite

`deploy/dreaming-live` and `spike/dreaming` become `e2e/conformance/`. The suite drives `factoryd` through the public HTTP API and two definition files. It does not link Dreaming types into `internal/`.

The suite covers today's offline rows: each preflight failure on its own case, and the secure case. With `FACTORY_TEST_DATABASE_URL` and `FACTORY_TEST_S3_ENDPOINT` set, it rehearses one draft pull request, a tiny ceiling, HTTP 402, cancel, a missing sentinel, a compile-only red transcript, and a second publish that stops before another token mint. Those cases run against the Dreaming example definition.

The same process then runs `examples/widget-factory/` and checks isolation:

| Check | Expected |
| --- | --- |
| Widget publish repo set to the Dreaming repo | Refused before a token mint. |
| Widget publish using Dreaming's issue key | Refused before a token mint. The body has none of Dreaming's publication fields. |
| Widget-scoped API key on a Dreaming run, approval, delivery, schedule, or work item | 404. Empty problem body. The deployment admin key is not this actor. |
| Widget run token on a Dreaming run route | 403 or 404. No body from the other factory. |
| Charges against the widget factory cap | Dreaming `factory_budgets` spent and reserved cents unchanged. |
| Widget attest pointed at `org/agents/...` | Absent object. The widget template is the only path that attests. |
| Dreaming secret name resolved on the widget factory | Unresolved. Fail closed. |
| Same secret name on both factories | Two values. The widget run receives only the widget value. |
| Widget key `POST /api/v1/agent/run` with `factory` set to Dreaming | 403 before insert. Dreaming's ceiling is unchanged. |
| Widget key `POST /api/v1/agent/schedules` with Dreaming's `factory_id` | 403 before insert. |
| Widget key `PUT` or `:apply` on either factory | 403. The admin key can apply both. |
| Widget run whose `repo` is the Dreaming repo | Assignment refused before `InstallationByRepo`. No token. |
| Widget key `GET /api/v1/agent/runs` with no query | Zero Dreaming runs. |
| Widget key `GET /api/v1/agent/runs?factory_id=` set to Dreaming | 403. |
| Widget key `GET /api/v1/agent/schedules` with no `factory` | Only widget schedules. |
| Widget key `GET /api/v1/agent/schedules?factory=` set to Dreaming | 403. |
| Widget key `GET /api/v1/factory` | Only the widget factory. |
| Widget key `GET /api/v1/agent/run-by-external-reference` for a Dreaming run | 404. Empty body. |

Private role bytes stay out of git. The live job checks a pin out into the runner temp directory, passes that directory as the cache input for that factory and commit, and does not upload it.

Deleted in the last PR: `deploy/dreaming-live/**`, `spike/dreaming/**`, `.github/workflows/dreaming-live.yml`. The CI step at `.github/workflows/ci.yml:275-276` runs `MODE=offline ./e2e/conformance/run_test.sh` and expects `conformance offline PASS`.

The replacement workflow is `workflow_dispatch` only. Inputs are `mode` (`preflight` or `live`), `confirm`, `factory_sha`, two definition paths, and `role_blobs` (JSON blob ids). `confirm` must equal `factory-live`. The workflow reads those files for the repos, the ref prefixes, and the pins. It does not embed a repo name. It is not dispatched by an agent. `mode=live` still checks the role repo out with a read token and runs the chain only after every preflight row passes.

## CI guard

`scripts/check-target-agnostic.sh` fails when a scanned file contains a denylist token. The scan is case-insensitive and is a substring match. It skips `*_test.go` and `testdata/`. TA-4 scans non-test `internal/` and `cmd/`. TA-5 adds `.github/workflows/` and `deploy/helm/` after `dreaming-live.yml` is deleted and the CI step no longer names `spike/dreaming` (`.github/workflows/ci.yml:275-276`). Scanning those trees in TA-4 fails on the Dreaming workflow that TA-5 removes. `dreamd` is a token. The token list is `scripts/target-denylist.txt`, one token per line, comments with `#`. Default tokens:

```text
dreaming
northstar
nils-aaberg
beatriz-salgado
viktor-halas
oskar-lindgren
tobias-lindqvist
dreamd
```

`roster`, `proof`, `SOUL`, and `HEARTBEAT` are not tokens. After this migration they are either mechanism words or definition data. `factoryEvidencePass` is not a token. The script prints `path:line` and exits non-zero. A unit test runs it against a temp tree that contains `dreaming` and expects failure, and against a clean tree and expects success. The `required` CI job runs the script. `shfmt` and `shellcheck` already cover `*.sh` (`.github/workflows/ci.yml:251-264`).

TA-4 does not scan `examples/`, `e2e/`, `deploy/`, `docs/`, or `.github/workflows/`. Those trees still name Dreaming until TA-5. TA-5 adds `.github/workflows/` and `deploy/helm/` to the same script. `examples/` and `e2e/` stay unscanned because they are the target fixtures. `internal/store/migrations/*.sql` stays unscanned so the one-shot `dreamd_` drop can name the old prefix. Every other non-test file under `internal/` and `cmd/` is scanned.

## Migration

Five PRs, merged in order. Offline tests stay green on each one. TA-1 through TA-4 keep `MODE=offline ./spike/dreaming/run_test.sh`. TA-5 replaces that command. No PR dispatches the live workflow. No PR edits `web/console/`.

### TA-1 — Schema only

Register the new `factory.yaml` keys, defaults, and validation. Runtime still uses the constants.

Paths: `docs/03-definition-schema.md`, `docs/13-defined-factory-contracts.md`, `docs/contracts/defined-factory/**`, `schemas/v1alpha1/factory.schema.json`, `internal/definition/**`, `internal/convert/**`, `deploy/helm/factory/crds/factory.dev_factories.yaml`.

Acceptance:

- A tree that sets `roleSource`, `native`, `publish`, `evidence`, `budget`, and `inference.rates` validates.
- A tree that omits them still validates, and legacy witness digests are unchanged.
- `absoluteMaxDepth: 9`, a publish repo absent from `repositories`, `publish.enabled: true` without `budget.ceilingCents`, a path template without `{slug}`, and an unknown key fail validation.
- `ValidateNativeTree` behavior is unchanged in this PR.

Verify:

```bash
go test ./internal/definition/ ./internal/convert/ -count=1
python3 docs/contracts/defined-factory/validate.py
MODE=offline ./spike/dreaming/run_test.sh
```

### TA-2 — Role source and hierarchy

Core reads paths, limits, root, implementer, and reviewer from the definition. The exact depth check, the synthetic-roster gate, the fixed role set, and the outside-manager check leave `internal/`.

Paths: `internal/definition/attest.go`, `internal/definition/pinned_roles.go`, `internal/definition/native_tree.go`, `internal/server/produced_bundle.go`, `internal/store/native_runtime.go`, `internal/api/native_runtime.go`, `internal/client/native_runtime.go`, matching `*_test.go`, `docs/14-native-coordination.md`. `deploy/dreaming-live/**` and `spike/dreaming/**` may change only so the offline driver supplies the Dreaming declaration through the API.

Acceptance:

- Dreaming's layout attests when the declaration names `org/roster.yaml` and `org/agents/{slug}/{file}`.
- The widget layout attests with `team/roles/{slug}/ROLE.md` and fails closed on `org/agents/...`.
- A tree of depth 0 and a tree of depth 3 validate when `maxDepth` allows them. Depth 9 fails.
- `treeDigest` JSON keys are unchanged. A legacy row still reloads with `Synthetic: true`, so its stored digest still matches. A new open persists the flag, and reload uses that stored value.
- `FACTORY_ROLE_SOURCE` is no longer read. The checkout is the per-factory cache.
- Publish still uses the old constants. This PR does not need the denylist to be clean if a comment remains; TA-3 removes the publish names. Prefer no new target names under `internal/`.

Verify:

```bash
go test ./internal/definition/ ./internal/server/ ./internal/store/ ./internal/api/ ./internal/client/ -count=1 -timeout 45m
MODE=offline ./spike/dreaming/run_test.sh
```

### TA-3 — Publish, evidence, price, secrets, and factory-scoped keys

Remove the publish constants, the process evidence paths, and the process price as the document a run reads. Resolve secrets by `(factory_id, name)`. Add factory-scoped API keys. Rename the evidence database prefix.

Paths: `internal/server/publish_draft.go`, `internal/server/disposable.go`, `internal/server/run_spend.go`, `internal/server/server.go`, `internal/server/native_assignment.go`, `internal/server/m4_wire.go`, `internal/server/m6_assign.go`, `internal/harness/materialize.go`, `internal/harness/mutate.go`, `internal/harness/evidence.go`, `internal/store/disposable.go`, `internal/store/draft_publication.go`, `internal/store/native_budget.go`, `internal/api/handler.go`, `cmd/factoryd/config.go`, a migration for `draft_publications.factory_id`, `factory_budgets`, and the one-shot `dreamd_` drop, matching tests, `docs/14-native-coordination.md`. `deploy/dreaming-live/**` may change so the offline rehearsal sets publish, evidence, rates, and the factory ceiling through the definition.

Acceptance:

- A factory with `publish.enabled: false` completes the root and writes no publication row.
- A request that names a repo outside the factory fails before a token mint.
- A second factory publishing the first factory's `(target_repo, issue_key)` fails before a token mint and the body contains none of the first row's fields.
- The reviewer SHA gate, draft-only create, scrubbed logs, and the single-repo token scopes still hold.
- The reviewer DSN is `FACTORY_EVIDENCE_DSN` and is absent from every other seat.
- New evidence databases and roles match `^factory_ev_[0-9a-f]{32}$`. The Go create path and `storeName` contain no `dreamd`. The one-shot migration drops only stored `dreamd_` names.
- Two factories in one test: a charge to the second factory's `factory_budgets` row leaves the first factory's spent and reserved cents unchanged. Deleting an invocation leaves the factory cap row in place.
- A secret name granted on both factories resolves to two values, from `FACTORY_SECRET_<factoryID>__<NAME>`. Factory B never receives factory A's value. An unprefixed `FACTORY_SECRET_<NAME>` does not resolve. `FACTORY_SECRET_ALLOWLIST` and `FACTORY_SECRET_MOUNT_DIR` stay configuration.
- A factory-scoped key for B gets 404 and an empty body on A's run, approval, delivery, schedule, and work item. The deployment admin key still reads both.
- A factory-scoped key for B gets 403 on `POST /api/v1/agent/run` and `POST /api/v1/agent/schedules` when the body names A, before insert or reserve. The same key gets 403 on `PUT` and `:apply` for every factory.
- The same key, on `GET /api/v1/agent/runs` and `GET /api/v1/agent/schedules` with no factory query, sees only B. A query that names A is 403. `GET /api/v1/factory` returns only B. `GET /api/v1/agent/run-by-external-reference` for A's run is an empty 404.
- `tokenRepos` (`internal/server/m4_wire.go:365-401`) is checked against the run's factory `repositories` before `InstallationByRepo` (`internal/server/m4_wire.go:351`). Any repo that the factory does not list refuses the assignment and mints nothing. A native `role_files` seat token is `contents: read` and `metadata: read`. Other app-mode `GH_TOKEN` values, including ordinary agent runs and improvement tasks, keep today's permissions. Improvement task tokens stay `contents: write` (`internal/server/m6_schedule_test.go:35`, `internal/server/m6_schedule_test.go:53`). The publish token stays `contents: write`, `pull_requests: write`, and `metadata: read`. `mintGitHubToken` (`internal/server/m4_wire.go:343-355`, called from `internal/server/assign.go:55-60`) applies the repository check to every mint and changes permissions only for a `role_files` seat.
- Applying a second factory while `FACTORY_GITHUB_TOKEN_MODE=pat` fails closed. `app` mode mints only for repos that factory lists.
- `inference.rates` frozen at open is the document `W` uses. Editing the process env after open does not change `W`.
- Non-test `internal/` and `cmd/`, other than `internal/store/migrations/*.sql`, contain none of the default denylist tokens.

Verify:

```bash
go test ./internal/server/ ./internal/harness/ ./internal/store/ ./internal/api/ -count=1 -timeout 45m
MODE=offline ./spike/dreaming/run_test.sh
```

### TA-4 — Denylist guard

Paths: `scripts/check-target-agnostic.sh`, `scripts/target-denylist.txt`, `scripts/check-target-agnostic_test.sh`, `.github/workflows/ci.yml`.

Acceptance:

- The script exits 0 on this tree and exits 1 when a temp `internal/` file contains `northstar` or `Oskar`.
- The `required` job runs it.
- `shfmt` and `shellcheck` are clean.
- The Dreaming offline command is unchanged.

Verify:

```bash
scripts/check-target-agnostic_test.sh
MODE=offline ./spike/dreaming/run_test.sh
```

### TA-5 — Conformance suite and the D6 re-point

Paths: `e2e/conformance/**`, `examples/dreaming-definition/**`, `examples/widget-factory/**`, `.github/workflows/conformance-live.yml`, `.github/workflows/ci.yml`. Delete `deploy/dreaming-live/**`, `spike/dreaming/**`, `.github/workflows/dreaming-live.yml`. Update the D6 paragraph in `docs/12-build-sequence.md` so the command is the conformance script.

The same PR renames the Helm proof worker. Paths for that slice only: `deploy/helm/factory/values.yaml`, `deploy/helm/factory/templates/_worker.tpl`, `deploy/helm/factory/tests/proof-worker.sh`, `deploy/helm/factory/tests/golden/**`, `docs/06-execution-backends.md`. `workers[].proof` becomes `securityProfile: locked`. The render rule stays the one in `_worker.tpl:125-154`. The example worker name, pool, and worker id become `locked`. This slice does not change `global.image.registry`, replica count, or any other chart value.

Acceptance:

- `MODE=offline ./e2e/conformance/run_test.sh` prints `conformance offline PASS`, covers the old P1–P14 failure rows and the secure case, and runs the widget isolation checks.
- The live workflow's inputs are two definition paths, `confirm`, `factory_sha`, and role blob ids. TA-5 does not dispatch it. The first dispatch is TF-1. The second path is an operator-supplied definition. `examples/widget-factory/` is not that file.
- With `.github/workflows/` and `deploy/helm/` added to the denylist roots, the script exits 0. That is the check that the new workflow and the chart contain no denylist token. It is not a separate manual reading.
- `helm template` fails when `securityProfile: locked` is combined with `setupCommand` or `teardownCommand`, and a locked worker's rendered config has neither key. A worker with no profile still renders a configured command. `workers[].proof` is gone. `global.image.registry` is still `ghcr.io/acme`.
- Example definitions contain paths and blob ids and do not contain role file bodies.
- The denylist script still exits 0.
- No step dispatches the live workflow.

Verify:

```bash
MODE=offline ./e2e/conformance/run_test.sh
scripts/check-target-agnostic.sh
```

TA-4 and TA-5 both edit `.github/workflows/ci.yml`, so they merge in that order. Example files may be written while TA-3 is in review. They merge only in TA-5, after the API they call exists.

## Out of scope

TA ends at TA-5. The following stay later work.

| Item | Where it goes |
| --- | --- |
| Filling `inference.rates` from a live gateway | Issue #217. The ceiling hard stop already stands without it. |
| Per-factory inference gateways | Later. `InferenceBaseURL` stays the deployment singleton. `inference.providers[].baseUrl` is the hook that later work would honor. |
| A distinct GitHub App key per factory | A later deployment feature. One App installed on each listed repo is enough for TA. |
| `dotted_to` as a delegation edge, and `allowAncestorShortcut` | A later contract change. Both stay refused. |
| Merge, ready, labels, review requests, or comments on the published pull request | Stays out. The publisher remains draft-only. |
| Console UI for many factories | Later. TA does not edit `web/console/`. |
| More than one `factoryd` replica sharing API-backed definitions | TF-2. Moved out of M8. `docs/10-kubernetes-and-helm.md:392-395`. Not TA-1 through TA-5. |
| Helm image publishing | TF-3. `global.image.registry` stays `ghcr.io/acme` (`deploy/helm/factory/values.yaml:5`) through TA-5. |
| First authorized live run | TF-1, the first step of TF, not the end of TA. Two definition inputs, live isolation, 2500 cent caps from the definitions and the run requests, draft pull requests only. |
| Untrusted operators sharing one database | Out of the product. One Helm release is one operator. |
| Scanner acceptance, Linear, and the improvement-loop reviewer pools | Unchanged milestones. |
| Dispatching the live workflow | The orchestrator, after preflight, on a merged SHA. Not an agent. |

## Decisions

The role layout is a native `factory.yaml` declaration. The Dreaming importer is an example authoring tool and is not on the load path. Reasons: every factory then loads through one validator; attestation stays a blob compare against declared paths; the denylist stays empty under `cmd/` and `internal/`; the open path does not branch on a repo's historical layout.

The definition is registered per factory (`git`, `api`, or `path`). A repo the factory edits is a target, not an implicit definition. A factory may point `definition.repo` at one listed repo. Apply still validates before serving, and the publish ref cannot move the definition ref.

One GitHub App per deployment. The factory's `repositories` list is the allowlist for every token that factory mints.

Global hierarchy cap is 8, today's `NativeAbsoluteMaxDepth`. Raising it is a contract change, not a definition field.

`InferenceBaseURL` stays one gateway per deployment. Draft-only publish stays. One GitHub App key per deployment stays.

The first authorized live run is TF-1, the first step of TF ([12](12-build-sequence.md#tf--two-factory-deployment)). It is not a sixth TA PR and it is not the end of TA. TA-5 leaves the workflow able to take two definition files and does not dispatch it.

## First authorized live run

One `factoryd` process. The workflow passes two definition files and does not name a Dreaming repo, ref, pin, or persona path. Each definition sets `budget.ceilingCents` to 2500. Each run request sets `ceilingCents` to 2500. A request above its definition fails before queue. The live driver reads 2500 from the definition and the run request. The number must not be a harness constant or a workflow constant.

The run opens one draft pull request per factory. Both stay drafts. Neither factory can read the other's runs, role bytes, or secret values. A charge against one factory leaves the other's `factory_budgets` spent and reserved cents unchanged. Counted spend on each factory stays at or under 2500 cents.

The offline widget fixture may use a different ceiling so a cross-charge is visible in the suite. The live pair uses 2500 on both definitions and both requests. That is the $25 cap.

## TF

TF owns the chart and the authorized live run. Tasks and the exit are in [12](12-build-sequence.md#tf--two-factory-deployment). TA does not publish images, does not add a replica, and does not dispatch the workflow.

| Item | Task |
| --- | --- |
| Helm image publishing | TF-3. The chart placeholder is `ghcr.io/acme` (`deploy/helm/factory/values.yaml:5`). |
| Multi-replica store | TF-2. Moved from M8. Operator mode still refuses `controlPlane.replicas` above 1 until that task (`docs/10-kubernetes-and-helm.md:392-395`). |
| Two definitions, live isolation, $25, draft only | TF-1. The workflow already takes the definition files after TA-5. |
