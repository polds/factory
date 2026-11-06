"""Legal worlds and invariant checks for defined-factory/1.

The JSON examples are the reviewable witnesses. Each accept document must
match the world declared here, and each check below must accept that world
and reject the paired illegal world. This module does not talk to a
service, a model, or a network.
"""

from __future__ import annotations

import hashlib
import json

CONTRACT_VERSION = "defined-factory/1"
BASELINE = "85812997d8e81b0209b678e583a7738b9d066966"

_ALPHA = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
_BODY = "01ARZ3NDEKTSV4RRFFQ69G5FA"


def ident(prefix: str, n: int) -> str:
    if len(_BODY) != 25:
        raise RuntimeError("id body width")
    return prefix + "_" + _BODY + _ALPHA[n]


INV = ident("inv", 0)
INV_B = ident("inv", 1)
TASK = ident("task", 2)
TASK_B = ident("task", 3)
RUN = ident("run", 4)
RUN_B = ident("run", 5)
RUN_C = ident("run", 6)
EXEC = ident("exec", 7)
EXEC_B = ident("exec", 8)
EXEC_C = ident("exec", 9)
NT_ROOT = ident("ntask", 10)
NT_LEAD = ident("ntask", 11)
NT_A = ident("ntask", 12)
NT_B = ident("ntask", 13)
MSG = ident("nmsg", 14)
DLV = ident("ndlv", 15)
DLV_B = ident("ndlv", 16)
SCH = ident("sch", 17)
NACT = ident("nact", 18)
NRES = ident("nres", 19)
NRES_B = ident("nres", 20)
NTOK = ident("ntok", 21)
NTOK_B = ident("ntok", 22)
POL = ident("pol", 23)
CONV = ident("conv", 24)

SHA_A = "a" * 64
SHA_B = "b" * 64
SHA_C = "c" * 64
GIT_A = "1" * 40
GIT_B = "2" * 40
GIT_C = "3" * 40
GIT_D = "4" * 40

PERSONAS = [
    {
        "name": "coordinator",
        "reportsTo": None,
        "role": "root",
        "spawnableBy": [],
    },
    {
        "name": "lead",
        "reportsTo": "coordinator",
        "role": "lead",
        "spawnableBy": ["coordinator"],
    },
    {
        "name": "specialist-a",
        "reportsTo": "lead",
        "role": "specialist",
        "spawnableBy": ["lead"],
    },
    {
        "name": "specialist-b",
        "reportsTo": "lead",
        "role": "specialist",
        "spawnableBy": ["lead"],
    },
]

CANONICAL_BODY = {
    "correlation": {"threadRef": "slack:T/C/1555"},
    "definitionRevision": "rev-applied-1",
    "inputs": {"instruction": "review"},
    "intent": "new",
    "priority": "interactive",
    "profile": {"name": "scan"},
    "sourceOccurrenceKey": "human:review:1",
    "target": {"pullRequest": 2213, "repository": "example/atum-core"},
}

LEAVES = [
    {"agent": "lead", "attemptId": "a1", "callId": "c1", "cents": 100},
    {"agent": "specialist-a", "attemptId": "a1", "callId": "c2", "cents": 400},
    {"agent": "specialist-a", "attemptId": "a2", "callId": "c2", "cents": 50},
    {"agent": "specialist-b", "attemptId": "a1", "callId": "c3", "cents": 250},
    {"agent": "specialist-b", "attemptId": "a1", "callId": "c3", "cents": 250},
]

LEDGER_EVENTS = [
    {"ledgerSeq": 1, "taskId": NT_ROOT, "type": "task.created"},
    {"ledgerSeq": 2, "taskId": NT_LEAD, "type": "task.created"},
    {"ledgerSeq": 3, "taskId": NT_A, "type": "task.assigned"},
    {"ledgerSeq": 4, "taskId": NT_A, "type": "result"},
    {"ledgerSeq": 5, "taskId": NT_A, "type": "task.completed"},
]

CAPACITY_STEPS = [
    {"op": "claim", "persona": "coordinator", "slotsUsed": 1},
    {"advancesPipeline": False, "op": "park", "persona": "coordinator", "slotsUsed": 0},
    {"op": "claim", "persona": "lead", "slotsUsed": 1},
    {"advancesPipeline": False, "op": "park", "persona": "lead", "slotsUsed": 0},
    {"op": "claim", "persona": "specialist-a", "slotsUsed": 1},
    {
        "advancesPipeline": False,
        "op": "complete",
        "persona": "specialist-a",
        "slotsUsed": 0,
    },
    {"op": "claim", "persona": "specialist-b", "slotsUsed": 1},
    {
        "advancesPipeline": False,
        "op": "complete",
        "persona": "specialist-b",
        "slotsUsed": 0,
    },
    {"op": "claim", "persona": "lead", "slotsUsed": 1},
    {"advancesPipeline": False, "op": "complete", "persona": "lead", "slotsUsed": 0},
    {"op": "claim", "persona": "coordinator", "slotsUsed": 1},
    {
        "advancesPipeline": True,
        "op": "complete",
        "persona": "coordinator",
        "slotsUsed": 0,
    },
]


def canonical_digest(body: object) -> str:
    raw = json.dumps(body, ensure_ascii=False, separators=(",", ":"), sort_keys=True)
    return hashlib.sha256(raw.encode()).hexdigest()


def _world(fields: dict) -> dict:
    return fields


EXPECT: dict[str, dict] = {
    "INV-REQUEST-SHAPE": _world(
        {
            "correlation": {"replyTo": "1555.1", "threadRef": "slack:T/C/1555"},
            "definitionRevision": "rev-applied-1",
            "inputs": {"instruction": "review"},
            "intent": "new",
            "priority": "interactive",
            "profile": {"name": "scan"},
            "sourceOccurrenceKey": "human:review:1",
            "target": {"pullRequest": 2213, "repository": "example/atum-core"},
        }
    ),
    "INV-REPLAY": _world(
        {
            "canonicalBody": CANONICAL_BODY,
            "canonicalDigest": canonical_digest(CANONICAL_BODY),
            "movedRefSha": GIT_C,
            "originalDefinitionDigest": SHA_A,
            "originalTargetSha": GIT_A,
            "receiptIdsEqual": True,
            "reloadedDefinitionDigest": SHA_B,
            "returnedDefinitionDigest": SHA_A,
            "returnedTargetSha": GIT_A,
            "sameCanonical": True,
            "sameKey": True,
            "writesOnReplay": 0,
        }
    ),
    "INV-CONFLICT": _world(
        {
            "code": "invocation_conflict",
            "httpStatus": 409,
            "originalReceiptUnchanged": True,
            "sameCanonical": False,
            "sameKey": True,
            "writes": 0,
        }
    ),
    "INV-INTENT-CONFLICT": _world(
        {
            "code": "invocation_conflict",
            "httpStatus": 409,
            "intents": ["new", "wake"],
            "sameKey": True,
            "writes": 0,
        }
    ),
    "INV-PRIORITY": _world({"human": "interactive", "schedule": "batch"}),
    "INV-CORRELATION": _world(
        {
            "invocationIdsDistinct": True,
            "sameThreadRef": "slack:T/C/1555",
            "sourceOccurrenceKeys": ["human:review:1", "human:review:2"],
            "threadRefIsIdentity": False,
        }
    ),
    "INV-PR-DISTINCT": _world(
        {
            "invocationIds": [INV, INV_B],
            "pullRequest": "https://github.com/example/atum-core/pull/2213",
            "sourceOccurrenceKeys": ["human:review:1", "human:review:2"],
        }
    ),
    "INV-PIN": _world(
        {
            "enforcesCheckout": True,
            "pins": [
                {
                    "baseSha": GIT_B,
                    "checkoutSha": GIT_A,
                    "headSha": GIT_A,
                    "kind": "pull_request",
                    "promptTextSha": GIT_C,
                },
                {
                    "checkoutSha": GIT_D,
                    "kind": "ref",
                    "promptTextSha": GIT_C,
                    "refSha": GIT_D,
                },
            ],
        }
    ),
    "INV-NO-FORGE": _world(
        {
            "accepted": True,
            "forgeTokenMinted": False,
            "requiresRepository": False,
            "targetPresent": False,
        }
    ),
    "INV-PRINCIPAL": _world(
        {
            "authenticated": False,
            "bodyPrincipalIgnored": True,
            "code": "principal_rejected",
            "httpStatus": 403,
            "principalFromBody": False,
            "writes": 0,
        }
    ),
    "INV-PROFILE": _world(
        {
            "injectionRejected": {
                "accepted": False,
                "code": "profile_rejected",
                "injected": ["budgetCeilingCents"],
                "writes": 0,
            },
            "selectionAccepted": {"accepted": True, "fields": ["name"]},
        }
    ),
    "INV-REPOSITORY": _world(
        {
            "allowlisted": False,
            "code": "repository_rejected",
            "httpStatus": 403,
            "writes": 0,
        }
    ),
    "INV-GRANT": _world(
        {
            "code": "grant_rejected",
            "executeTargetRequested": True,
            "granted": False,
            "httpStatus": 403,
            "writes": 0,
        }
    ),
    "INV-SCHEDULE-KEY": _world(
        {
            "deliveredAt": "2026-11-09T00:04:00Z",
            "dueSlot": "2026-11-09T00:00:00Z",
            "keyUsesDeliveredAt": False,
            "keyUsesDueSlot": True,
            "occurrenceKey": "schedule:" + SCH + ":slot:2026-11-09T00:00:00Z",
            "scheduleId": SCH,
        }
    ),
    "INV-SCHEDULE-REPLAY": _world(
        {
            "createsRun": False,
            "receiptIdsEqual": True,
            "sameDueSlot": True,
            "sameScheduleId": True,
        }
    ),
    "INV-OVERLAP": _world(
        {
            "continuedWorkItemId": None,
            "countsAsCompletedScan": False,
            "outcome": "skipped_overlap",
            "policy": "forbid",
            "priorState": "running",
            "runStarted": False,
            "slotState": "skipped",
        }
    ),
    "INV-TWO-SLOTS": _world(
        {
            "distinct": True,
            "itemCount": 2,
            "priorFinished": True,
            "slots": ["2026-11-02T00:00:00Z", "2026-11-09T00:00:00Z"],
            "workItemIds": [TASK, TASK_B],
        }
    ),
    "INV-LEGACY-CONTINUE": _world(
        {
            "continuation": True,
            "occurrenceMode": "legacy_continue",
            "slotCount": 2,
            "workItemCount": 1,
        }
    ),
    "INV-LEADER": _world(
        {
            "crashBeforeCommitCreatesAtMostOne": True,
            "lateDeliverySameSlotCreatesAnother": False,
            "receiptStable": True,
            "store": "postgres",
        }
    ),
    "INV-REPORT-ZERO": _world(
        {
            "countsAsSuccess": True,
            "coverageStatus": "present",
            "findingsCount": 0,
            "reportPresent": True,
            "status": "succeeded",
        }
    ),
    "INV-PR-SEPARATION": _world(
        {
            "inputPullRequest": "https://github.com/example/atum-core/pull/2213",
            "outputPullRequest": None,
            "satisfiesPullRequestRequires": False,
        }
    ),
    "INV-REQUIRES-PR": _world(
        {
            "code": "no_work_product",
            "inputPullRequest": "https://github.com/example/atum-core/pull/2213",
            "outputPullRequest": None,
            "requires": ["pullRequest"],
            "status": "failed",
        }
    ),
    "INV-REQUIRES-EMPTY": _world(
        {
            "pullRequestRequired": False,
            "reportRequired": False,
            "requires": [],
            "status": "succeeded",
        }
    ),
    "INV-REQUIRES-REPORT": _world(
        {
            "requires": ["report"],
            "storagePresent": True,
            "validatorPresent": True,
        }
    ),
    "INV-NO-FINDINGS-ENUM": _world(
        {
            "artifactsEnum": False,
            "findingsEnum": False,
            "allowedRequires": ["pullRequest", "report"],
        }
    ),
    "INV-REPORT-MISSING": _world(
        {
            "code": "report_missing",
            "reportPresent": False,
            "requires": ["report"],
            "status": "failed",
            "succeeded": False,
        }
    ),
    "INV-UNAUTH-READ": _world(
        {
            "bodyBytes": 0,
            "code": "result_unauthorized",
            "httpStatus": 403,
        }
    ),
    "INV-PATH": _world(
        {
            "code": None,
            "path": "reports/scan.json",
            "published": True,
        }
    ),
    "INV-SYMLINK": _world(
        {
            "code": "result_symlink_rejected",
            "path": "reports/scan.json",
            "published": False,
            "symlink": True,
        }
    ),
    "INV-SIZE": _world(
        {
            "artifactMaxBytes": 16777216,
            "code": None,
            "published": True,
            "reportBytes": 1024,
            "reportMaxBytes": 1048576,
        }
    ),
    "INV-SECRET-PUB": _world(
        {
            "canary": "CANARY-SYNTHETIC",
            "code": "secret_publication_rejected",
            "published": False,
            "scanHit": True,
        }
    ),
    "INV-TREE": _world(
        {
            "absoluteMaxDepth": 8,
            "acyclic": True,
            "allowAncestorShortcut": False,
            "atumRoster": False,
            "connected": True,
            "delegation": "direct_manager",
            "maxDepth": 2,
            "nativeHierarchy": True,
            "note": "synthetic coordinator/lead/two-specialist tree; not an Atum roster",
            "personas": PERSONAS,
            "rootCount": 1,
            "synthetic": True,
        }
    ),
    "INV-LEGACY-HIERARCHY": _world(
        {
            "enforced": False,
            "nativeHierarchy": False,
            "reportsToOmitted": True,
            "valid": True,
        }
    ),
    "INV-DEPTH": _world(
        {
            "absoluteMaxDepth": 8,
            "accepted": True,
            "code": None,
            "depth": 2,
            "maxDepth": 2,
        }
    ),
    "INV-DELEGATION": _world(
        {
            "allowAncestorShortcut": False,
            "allowed": True,
            "code": None,
            "delegator": "lead",
            "reportsTo": "lead",
            "spawnableBy": ["lead"],
            "target": "specialist-a",
        }
    ),
    "INV-NOT-PARENT-RUN": _world(
        {
            "delegationEdgeId": NT_A,
            "parentRunId": RUN,
            "sameIdentifier": False,
            "stagePredecessor": True,
        }
    ),
    "INV-NO-SECRET-FROM-REPORTS": _world(
        {
            "accessFromReporting": False,
            "repoGrant": False,
            "reportsTo": "lead",
            "secretGrant": False,
        }
    ),
    "INV-IDENTITIES": _world(
        {
            "activationId": NACT,
            "assignee": "specialist-a",
            "blockerIds": [NT_B],
            "executionId": EXEC,
            "taskId": NT_A,
        }
    ),
    "INV-STAGE-OWNER": _world(
        {
            "childAdvances": False,
            "childStageEqualsWorkItemStage": True,
            "rootAdvances": True,
            "stageOwner": "coordinator",
        }
    ),
    "INV-CANONICAL": _world(
        {
            "oneCanonicalRecord": True,
            "parallelStores": False,
            "projection": "per_assignee_inbox",
        }
    ),
    "INV-BODY": _world({"accepted": True, "bodyByteLength": 8192, "maxBytes": 8192}),
    "INV-ACK": _world(
        {
            "ackAfterDurableApply": True,
            "idempotent": True,
            "principalScoped": True,
            "readAcks": False,
            "taskScoped": True,
        }
    ),
    "INV-READ-NOT-ACK": _world({"acked": False, "longPoll": True, "read": True}),
    "INV-DROP": _world(
        {
            "applyCount": 1,
            "deliveries": [
                {"acked": False, "deliveryId": DLV, "responseDropped": True},
                {"acked": True, "deliveryId": DLV_B, "responseDropped": False},
            ],
            "lost": False,
            "messageId": MSG,
        }
    ),
    "INV-DUP-ACK": _world({"ackCount": 2, "applyCount": 1, "secondIdempotent": True}),
    "INV-LATE-COMMIT": _world(
        {
            "events": LEDGER_EVENTS,
            "factorySeqIsInvocationOrder": False,
            "orderMechanism": "invocation_ledger_row_lock",
            "runEventSeqScope": "per_run",
            "runLocalSeqs": [
                {"runId": RUN, "seq": 1},
                {"runId": RUN_B, "seq": 1},
            ],
            "sseCursor": "run_events.seq",
        }
    ),
    "INV-SSE": _world(
        {
            "cursor": "Last-Event-ID",
            "perRunSeqPreserved": True,
            "switchesToInvocationLedger": False,
        }
    ),
    "INV-CLAIM": _world(
        {
            "code": "claim_fenced",
            "fenceAfter": 1,
            "firstFence": 1,
            "holderAfter": "lead",
            "secondAccepted": False,
        }
    ),
    "INV-PAGINATION": _world(
        {
            "accepted": True,
            "cursorOpaque": True,
            "defaultLimit": 50,
            "limit": 50,
            "maxLimit": 100,
        }
    ),
    "INV-COALESCE": _world({"durableWakeRecords": 1, "taskId": NT_A, "wakeIntents": 3}),
    "INV-CAPACITY": _world({"capacity": 1, "steps": CAPACITY_STEPS}),
    "INV-PARK": _world(
        {
            "capacityReleased": True,
            "durableBlockers": True,
            "state": "parked",
        }
    ),
    "INV-STEER": _world(
        {
            "delivery": "pending",
            "negotiatedSteer": False,
            "running": True,
        }
    ),
    "INV-COMPLETED-WAKE": _world(
        {
            "linkedFromRunId": RUN,
            "newRunId": RUN_B,
            "priorRunId": RUN,
            "priorTerminal": "succeeded",
            "priorTerminalMutated": False,
            "sameRun": False,
        }
    ),
    "INV-RETRY-EXEC": _world(
        {
            "newExecutionId": EXEC_B,
            "priorExecutionId": EXEC,
            "runId": RUN,
            "sameExecution": False,
            "sameRun": True,
        }
    ),
    "INV-PARK-CONTINUE": _world(
        {
            "newExecution": True,
            "newExecutionId": EXEC_C,
            "runId": RUN_C,
            "sameRun": True,
        }
    ),
    "INV-NO-RESURRECT": _world(
        {
            "autoResurrect": False,
            "code": "cancelled_no_resurrect",
            "newRun": False,
        }
    ),
    "INV-SUBTREE-CANCEL": _world(
        {
            "action": "cancel",
            "childAdvancesPipeline": False,
            "descendants": [NT_LEAD, NT_A, NT_B],
            "laterSpawn": "spawn_denied",
            "resulting": "cancelled",
            "root": NT_ROOT,
        }
    ),
    "INV-BOARD-RESUME": _world(
        {
            "actor": "api_key:ops",
            "audited": True,
            "authorized": True,
            "createsLinkedRun": True,
            "from": "cancelled",
            "priorTerminalMutated": False,
        }
    ),
    "INV-FAIL-CHILD": _world(
        {
            "childStatus": "failed",
            "parentStatus": "failed",
            "policy": "fail_parent",
            "replacementSpawned": False,
            "synthesized": False,
        }
    ),
    "INV-JOIN": _world(
        {
            "requiredChildrenLive": True,
            "synthesized": False,
            "unresolvedChild": True,
        }
    ),
    "INV-DELEGATE-IDEMPOTENT": _world(
        {
            "childCount": 1,
            "duplicateDelivery": True,
            "reservationCount": 1,
            "sameKey": True,
        }
    ),
    "INV-BUDGET-DEDUP": _world(
        {
            "countedCents": 250,
            "duplicateAttemptCounted": False,
            "failedAttemptCounted": True,
            "unit": "cents",
        }
    ),
    "INV-ROLLUP": _world(
        {
            "forbiddenInclusiveSum": 1600,
            "leaves": LEAVES,
            "rollups": {
                "factory": 800,
                "invocation": 800,
                "lead": 800,
                "leadAgent": 100,
                "specialist-a": 450,
                "specialist-b": 250,
            },
        }
    ),
    "INV-RESERVE": _world(
        {
            "atomic": True,
            "lockOrder": "scope_id",
            "partialOnFailure": False,
            "scopes": ["factory", "invocation", "lead", "agent"],
        }
    ),
    "INV-CONCURRENT": _world(
        {
            "attempts": [
                {"cents": 2000, "committed": True, "id": NRES},
                {
                    "cents": 2000,
                    "code": "budget_exceeded",
                    "committed": False,
                    "id": NRES_B,
                },
            ],
            "ceilingCents": 2500,
            "partialReserve": False,
            "reservedCents": 2000,
            "spentCents": 0,
        }
    ),
    "INV-ALERT80": _world(
        {
            "alerts": 2,
            "ceilingCents": 1000,
            "charges": [900, -200, 200],
            "thresholdPercent": 80,
        }
    ),
    "INV-MISSING-USAGE": _world(
        {
            "admitNew": False,
            "deadlinePassed": True,
            "overshootBoundCents": 300,
            "reservedCents": 300,
            "spentCents": 0,
            "usageArrived": False,
        }
    ),
    "INV-OVERSHOOT": _world(
        {
            "ceilingCents": 2500,
            "gatewayCapCents": 2500,
            "overshootBeyondCeilingCents": 0,
            "reservedInFlightCents": 300,
        }
    ),
    "INV-LATE-CHARGE": _world(
        {
            "code": "usage_late_over_reservation",
            "paused": True,
            "quarantinedCents": 150,
            "reportedCents": 450,
            "reservedCents": 300,
            "spentCents": 300,
        }
    ),
    "INV-GATEWAY": _world(
        {
            "engagementCapCents": 2500,
            "gatewayCapCents": 2500,
            "gatewayCapLeqCeiling": True,
            "isolation": "per_engagement",
            "sharedKey": False,
        }
    ),
    "INV-SHARED-KEY": _world(
        {
            "guaranteesCeiling": False,
            "inFlightUncapped": True,
            "perEngagementAllocation": False,
            "sharedKey": True,
            "spawnRefused": True,
        }
    ),
    "INV-BUDGET-OVERRIDE": _world(
        {
            "accepted": True,
            "actor": "api_key:ops",
            "auditCommitted": True,
            "ceilingCents": 4000,
            "code": None,
            "priorCeilingCents": 2500,
            "spentDeltaCents": 0,
            "writes": 1,
        }
    ),
    "INV-TOKEN": _world(
        {
            "childCanDispatch": False,
            "childGrants": ["ack.write", "inbox.read"],
            "crossInvocation": False,
            "parentGrants": ["ack.write", "inbox.read", "roster.dispatch"],
            "specialistDispatch": False,
        }
    ),
    "INV-NO-DISPATCH": _world(
        {
            "dispatchAllowed": False,
            "dispatcherRole": "specialist",
            "leadMayDispatch": True,
        }
    ),
    "INV-WORKFLOW": _world(
        {
            "internalWorkersAreRoster": False,
            "internalWorkersInSession": True,
            "workflowMainLoopOnly": True,
        }
    ),
    "INV-SECRET-ALLOWLIST": _world(
        {
            "allowlist": ["SCAN_GITHUB_TOKEN"],
            "code": None,
            "name": "SCAN_GITHUB_TOKEN",
            "resolved": True,
        }
    ),
    "INV-SECRET-REFRESH": _world(
        {
            "lookupPerInjection": True,
            "lookups": 2,
            "rotatesRunningProcess": False,
            "subPathMount": False,
        }
    ),
    "INV-SECRET-UNSAFE": _world(
        {
            "code": "secret_ref_rejected",
            "missingRef": True,
            "resolved": False,
            "unsafePath": "../secrets/token",
        }
    ),
    "INV-REDACTION": _world(
        {
            "coversMcp": True,
            "retainedLiterals": ["CANARY-NEW", "CANARY-OLD"],
        }
    ),
    "INV-ISOLATION": _world(
        {
            "envOnlyIsolationAccepted": False,
            "evidenceClass": "adapter",
            "liveBlockedIfUnproven": True,
            "outcome": "isolated",
        }
    ),
    "INV-READONLY-TOKEN": _world(
        {
            "contents": "read",
            "metadata": "read",
            "pullRequests": "none",
            "scannerTask": True,
        }
    ),
    "INV-UNTRUSTED": _world(
        {
            "broadSecretMount": False,
            "dockerSocket": False,
            "foreignCheckoutWrite": False,
        }
    ),
    "INV-TASK-REF": _world(
        {
            "linearIssue": None,
            "mode": "native",
            "requiresLinear": False,
            "taskRef": NT_ROOT,
        }
    ),
    "INV-LOCAL-CLI": _world(
        {
            "mode": "local_cli",
            "requiresLinear": False,
            "requiresTaskRef": False,
        }
    ),
    "INV-ART": _world(
        {
            "perItemMount": True,
            "sharedWritableRoot": False,
        }
    ),
    "INV-RETENTION": _world(
        {
            "reportSurvivesWorkspaceCleanup": True,
            "retention": "durable_result_row",
        }
    ),
    "INV-BRIDGE": _world(
        {
            "blocksExecution": False,
            "echoSuppressed": True,
            "enabled": False,
            "outageQueues": "reporting",
            "owners": ["factory-outbound-bridge"],
            "replaySuppressed": True,
            "sendAuthorized": False,
            "sendCount": 0,
        }
    ),
    "INV-SECURITY-GATE": _world(
        {
            "backend": "docker",
            "cniProved": False,
            "g14ActualBackend": False,
            "g18ActualBackend": False,
            "gatewayCapProven": False,
            "helmRenderPassed": True,
            "hostFirewallEnforced": False,
            "kindPassed": True,
            "paidProbeAllowed": False,
        }
    ),
    "INV-ATTESTATION": _world(
        {
            "bound": {
                "adapterDigest": SHA_A,
                "backendId": "docker-host-a",
                "codeDigest": SHA_B,
                "configDigest": SHA_C,
                "imageDigest": SHA_A,
            },
            "invalidatesOn": [
                "adapterDigest",
                "backendId",
                "codeDigest",
                "configDigest",
                "imageDigest",
                "secretPolicy",
            ],
            "observed": {
                "adapterDigest": SHA_A,
                "backendId": "docker-host-a",
                "codeDigest": SHA_B,
                "configDigest": SHA_C,
                "imageDigest": SHA_A,
            },
            "valid": True,
        }
    ),
    "INV-LEGACY-ROUTES": _world(
        {
            "agentRunPath": "/api/v1/agent/run",
            "agentRunRemainsBare": True,
            "invocationPath": "/api/v1/factory/{uid}/invocations",
            "postTasksRemainsWorkItem": True,
            "tasksPath": "/api/v1/factory/{uid}/tasks",
        }
    ),
}


def _flip(inv: str, field: str, value: object) -> dict:
    doc = json.loads(json.dumps(EXPECT[inv]))
    doc[field] = value
    return doc


def _nested_flip(inv: str, path: tuple[str, ...], value: object) -> dict:
    doc = json.loads(json.dumps(EXPECT[inv]))
    cursor = doc
    for key in path[:-1]:
        cursor = cursor[key]
    cursor[path[-1]] = value
    return doc


NEGATIVES: dict[str, dict] = {
    "INV-REQUEST-SHAPE": {
        "budgetCeilingCents": 2500,
        "intent": "new",
        "priority": "interactive",
        "profile": {"name": "scan"},
        "sourceOccurrenceKey": "human:review:1",
    },
    "INV-REPLAY": _flip("INV-REPLAY", "returnedDefinitionDigest", SHA_B),
    "INV-CONFLICT": _flip("INV-CONFLICT", "writes", 1),
    "INV-INTENT-CONFLICT": _flip("INV-INTENT-CONFLICT", "writes", 1),
    "INV-PRIORITY": _flip("INV-PRIORITY", "schedule", "interactive"),
    "INV-CORRELATION": _flip("INV-CORRELATION", "threadRefIsIdentity", True),
    "INV-PR-DISTINCT": _flip("INV-PR-DISTINCT", "invocationIds", [INV, INV]),
    "INV-PIN": _nested_flip("INV-PIN", ("pins", 0, "checkoutSha"), GIT_C),
    "INV-NO-FORGE": _flip("INV-NO-FORGE", "forgeTokenMinted", True),
    "INV-PRINCIPAL": _flip("INV-PRINCIPAL", "writes", 1),
    "INV-PROFILE": _nested_flip("INV-PROFILE", ("injectionRejected", "accepted"), True),
    "INV-REPOSITORY": _flip("INV-REPOSITORY", "writes", 1),
    "INV-GRANT": _flip("INV-GRANT", "writes", 1),
    "INV-SCHEDULE-KEY": _flip("INV-SCHEDULE-KEY", "keyUsesDeliveredAt", True),
    "INV-SCHEDULE-REPLAY": _flip("INV-SCHEDULE-REPLAY", "createsRun", True),
    "INV-OVERLAP": _flip("INV-OVERLAP", "continuedWorkItemId", TASK),
    "INV-TWO-SLOTS": _flip("INV-TWO-SLOTS", "itemCount", 1),
    "INV-LEGACY-CONTINUE": _flip("INV-LEGACY-CONTINUE", "workItemCount", 2),
    "INV-LEADER": _flip("INV-LEADER", "receiptStable", False),
    "INV-REPORT-ZERO": _flip("INV-REPORT-ZERO", "countsAsSuccess", False),
    "INV-PR-SEPARATION": _flip(
        "INV-PR-SEPARATION", "satisfiesPullRequestRequires", True
    ),
    "INV-REQUIRES-PR": _flip("INV-REQUIRES-PR", "status", "succeeded"),
    "INV-REQUIRES-EMPTY": _flip("INV-REQUIRES-EMPTY", "pullRequestRequired", True),
    "INV-REQUIRES-REPORT": _flip("INV-REQUIRES-REPORT", "validatorPresent", False),
    "INV-NO-FINDINGS-ENUM": {"requires": ["findings"]},
    "INV-REPORT-MISSING": _flip("INV-REPORT-MISSING", "succeeded", True),
    "INV-UNAUTH-READ": _flip("INV-UNAUTH-READ", "bodyBytes", 12),
    "INV-PATH": _flip("INV-PATH", "path", "../secret"),
    "INV-SYMLINK": _flip("INV-SYMLINK", "published", True),
    "INV-SIZE": _flip("INV-SIZE", "reportBytes", 1048577),
    "INV-SECRET-PUB": _flip("INV-SECRET-PUB", "published", True),
    "INV-TREE": _flip("INV-TREE", "acyclic", False),
    "INV-LEGACY-HIERARCHY": _flip("INV-LEGACY-HIERARCHY", "enforced", True),
    "INV-DEPTH": _flip("INV-DEPTH", "depth", 3),
    "INV-DELEGATION": _flip("INV-DELEGATION", "delegator", "coordinator"),
    "INV-NOT-PARENT-RUN": _flip("INV-NOT-PARENT-RUN", "sameIdentifier", True),
    "INV-NO-SECRET-FROM-REPORTS": _flip(
        "INV-NO-SECRET-FROM-REPORTS", "accessFromReporting", True
    ),
    "INV-IDENTITIES": _flip("INV-IDENTITIES", "activationId", EXEC),
    "INV-STAGE-OWNER": _flip("INV-STAGE-OWNER", "childAdvances", True),
    "INV-CANONICAL": _flip("INV-CANONICAL", "parallelStores", True),
    "INV-BODY": _flip("INV-BODY", "bodyByteLength", 8193),
    "INV-ACK": _flip("INV-ACK", "readAcks", True),
    "INV-READ-NOT-ACK": _flip("INV-READ-NOT-ACK", "acked", True),
    "INV-DROP": _nested_flip("INV-DROP", ("deliveries", 0, "acked"), True),
    "INV-DUP-ACK": _flip("INV-DUP-ACK", "applyCount", 2),
    "INV-LATE-COMMIT": _nested_flip(
        "INV-LATE-COMMIT", ("events", 3, "type"), "task.completed"
    ),
    "INV-SSE": _flip("INV-SSE", "switchesToInvocationLedger", True),
    "INV-CLAIM": _flip("INV-CLAIM", "secondAccepted", True),
    "INV-PAGINATION": _flip("INV-PAGINATION", "limit", 101),
    "INV-COALESCE": _flip("INV-COALESCE", "durableWakeRecords", 3),
    "INV-CAPACITY": _flip(
        "INV-CAPACITY",
        "steps",
        [
            {"op": "claim", "persona": "coordinator", "slotsUsed": 1},
            {"op": "claim", "persona": "lead", "slotsUsed": 2},
        ],
    ),
    "INV-PARK": _flip("INV-PARK", "capacityReleased", False),
    "INV-STEER": _flip("INV-STEER", "delivery", "steer"),
    "INV-COMPLETED-WAKE": _flip("INV-COMPLETED-WAKE", "sameRun", True),
    "INV-RETRY-EXEC": _flip("INV-RETRY-EXEC", "sameRun", False),
    "INV-PARK-CONTINUE": _flip("INV-PARK-CONTINUE", "sameRun", False),
    "INV-NO-RESURRECT": _flip("INV-NO-RESURRECT", "autoResurrect", True),
    "INV-SUBTREE-CANCEL": _flip("INV-SUBTREE-CANCEL", "laterSpawn", "allowed"),
    "INV-BOARD-RESUME": _flip("INV-BOARD-RESUME", "audited", False),
    "INV-FAIL-CHILD": _flip("INV-FAIL-CHILD", "synthesized", True),
    "INV-JOIN": _flip("INV-JOIN", "synthesized", True),
    "INV-DELEGATE-IDEMPOTENT": _flip("INV-DELEGATE-IDEMPOTENT", "childCount", 2),
    "INV-BUDGET-DEDUP": _flip("INV-BUDGET-DEDUP", "duplicateAttemptCounted", True),
    "INV-ROLLUP": _nested_flip("INV-ROLLUP", ("rollups", "factory"), 1600),
    "INV-RESERVE": _flip("INV-RESERVE", "partialOnFailure", True),
    "INV-CONCURRENT": _flip("INV-CONCURRENT", "reservedCents", 4000),
    "INV-ALERT80": _flip("INV-ALERT80", "alerts", 1),
    "INV-MISSING-USAGE": _flip("INV-MISSING-USAGE", "admitNew", True),
    "INV-OVERSHOOT": _flip("INV-OVERSHOOT", "overshootBeyondCeilingCents", 300),
    "INV-LATE-CHARGE": _flip("INV-LATE-CHARGE", "spentCents", 450),
    "INV-GATEWAY": _flip("INV-GATEWAY", "gatewayCapCents", 5000),
    "INV-SHARED-KEY": _flip("INV-SHARED-KEY", "guaranteesCeiling", True),
    "INV-BUDGET-OVERRIDE": _flip("INV-BUDGET-OVERRIDE", "auditCommitted", False),
    "INV-TOKEN": _flip("INV-TOKEN", "childCanDispatch", True),
    "INV-NO-DISPATCH": _flip("INV-NO-DISPATCH", "dispatchAllowed", True),
    "INV-WORKFLOW": _flip("INV-WORKFLOW", "workflowMainLoopOnly", False),
    "INV-SECRET-ALLOWLIST": _flip("INV-SECRET-ALLOWLIST", "name", "OPENAI_API_KEY"),
    "INV-SECRET-REFRESH": _flip("INV-SECRET-REFRESH", "rotatesRunningProcess", True),
    "INV-SECRET-UNSAFE": _flip("INV-SECRET-UNSAFE", "resolved", True),
    "INV-REDACTION": _flip("INV-REDACTION", "retainedLiterals", ["CANARY-NEW"]),
    "INV-ISOLATION": _flip("INV-ISOLATION", "envOnlyIsolationAccepted", True),
    "INV-READONLY-TOKEN": _flip("INV-READONLY-TOKEN", "contents", "write"),
    "INV-UNTRUSTED": _flip("INV-UNTRUSTED", "dockerSocket", True),
    "INV-TASK-REF": _flip("INV-TASK-REF", "requiresLinear", True),
    "INV-LOCAL-CLI": _flip("INV-LOCAL-CLI", "requiresLinear", True),
    "INV-ART": _flip("INV-ART", "sharedWritableRoot", True),
    "INV-RETENTION": _flip("INV-RETENTION", "reportSurvivesWorkspaceCleanup", False),
    "INV-BRIDGE": _flip("INV-BRIDGE", "sendCount", 1),
    "INV-SECURITY-GATE": _flip("INV-SECURITY-GATE", "paidProbeAllowed", True),
    "INV-ATTESTATION": _nested_flip(
        "INV-ATTESTATION", ("observed", "configDigest"), SHA_A
    ),
    "INV-LEGACY-ROUTES": _flip("INV-LEGACY-ROUTES", "postTasksRemainsWorkItem", False),
}

SCHEMA_REJECT = {
    "INV-REQUEST-SHAPE": "request",
    "INV-NO-FINDINGS-ENUM": "requires",
}

# Semantic checks that are formulas rather than exact equality.
# Exact worlds still run through `formulas` when a key is present.


def _dedup_leaves(leaves: list[dict]) -> list[dict]:
    seen: dict[tuple[str, str], dict] = {}
    for leaf in leaves:
        key = (leaf["callId"], leaf["attemptId"])
        if key in seen:
            if (
                seen[key]["cents"] != leaf["cents"]
                or seen[key]["agent"] != leaf["agent"]
            ):
                raise ValueError("usage key conflict")
            continue
        seen[key] = leaf
    return list(seen.values())


def _leaf_sum(leaves: list[dict]) -> int:
    return sum(leaf["cents"] for leaf in _dedup_leaves(leaves))


def _alerts(ceiling: int, charges: list[int]) -> int:
    spent = 0
    alerts = 0
    episode = False
    for charge in charges:
        spent += charge
        crossed = spent * 100 >= ceiling * 80
        if crossed and not episode:
            alerts += 1
            episode = True
        elif not crossed:
            episode = False
    return alerts


def _graph_ok(personas: list[dict]) -> bool:
    names = [p["name"] for p in personas]
    if len(names) != len(set(names)):
        return False
    by = {p["name"]: p for p in personas}
    roots = [p for p in personas if p["reportsTo"] is None]
    if len(roots) != 1:
        return False
    seen: set[str] = set()

    def walk(name: str, stack: set[str]) -> bool:
        if name in stack:
            return False
        if name in seen:
            return True
        if name not in by:
            return False
        seen.add(name)
        parent = by[name]["reportsTo"]
        if parent is None:
            return True
        return walk(parent, stack | {name})

    if not all(walk(name, set()) for name in names):
        return False
    return seen == set(names)


def _capacity_ok(steps: list[dict], capacity: int) -> bool:
    slots = 0
    holding: set[str] = set()
    parked: set[str] = set()
    done: set[str] = set()
    advanced = False
    for step in steps:
        persona = step["persona"]
        op = step["op"]
        if op == "claim":
            if slots >= capacity or persona in holding:
                return False
            if persona.startswith("specialist") and (
                "lead" in holding or "coordinator" in holding
            ):
                return False
            if persona == "lead" and "coordinator" in holding:
                return False
            slots += 1
            holding.add(persona)
            parked.discard(persona)
        elif op == "park":
            if persona not in holding:
                return False
            holding.remove(persona)
            parked.add(persona)
            slots -= 1
        elif op == "complete":
            if persona not in holding:
                return False
            holding.remove(persona)
            done.add(persona)
            slots -= 1
            if step.get("advancesPipeline"):
                if persona != "coordinator" or advanced:
                    return False
                advanced = True
            elif persona == "coordinator":
                return False
        else:
            return False
        if step.get("slotsUsed") != slots or slots < 0 or slots > capacity:
            return False
    needed = {"coordinator", "lead", "specialist-a", "specialist-b"}
    return advanced and done == needed and slots == 0


def _ledger_ok(events: list[dict]) -> bool:
    seqs = [event["ledgerSeq"] for event in events]
    if seqs != list(range(1, len(seqs) + 1)):
        return False
    result = [event for event in events if event["type"] == "result"]
    done = [event for event in events if event["type"] == "task.completed"]
    if len(result) != 1 or len(done) != 1:
        return False
    if result[0]["taskId"] != done[0]["taskId"]:
        return False
    return result[0]["ledgerSeq"] < done[0]["ledgerSeq"]


def _occurrence_key(schedule_id: str, due_slot: str) -> str:
    return f"schedule:{schedule_id}:slot:{due_slot}"


def _safe_path(path: str) -> bool:
    parts = path.split("/") if path else []
    return (
        bool(path)
        and not path.startswith("/")
        and "\\" not in path
        and ".." not in parts
    )


def formula_violations(inv: str, doc: dict) -> list[str]:
    """Return violation codes. Empty means the world is legal."""
    try:
        ok = _formula(inv, doc)
    except (KeyError, TypeError, ValueError):
        return ["fixture_incomplete"]
    if ok:
        return []
    return [inv]


def _formula(inv: str, doc: dict) -> bool:
    if inv == "INV-REPLAY":
        digest_ok = doc["canonicalDigest"] == canonical_digest(doc["canonicalBody"])
        pins_ok = (
            doc["sameKey"] is True
            and doc["sameCanonical"] is True
            and doc["writesOnReplay"] == 0
            and doc["receiptIdsEqual"] is True
            and doc["originalDefinitionDigest"] == SHA_A
            and doc["returnedDefinitionDigest"] == doc["originalDefinitionDigest"]
            and doc["reloadedDefinitionDigest"] == SHA_B
            and doc["originalTargetSha"] == GIT_A
            and doc["returnedTargetSha"] == doc["originalTargetSha"]
            and doc["movedRefSha"] == GIT_C
            and doc["canonicalBody"] == CANONICAL_BODY
        )
        return digest_ok and pins_ok
    if inv == "INV-SCHEDULE-KEY":
        expect = _occurrence_key(doc["scheduleId"], doc["dueSlot"])
        return (
            doc["scheduleId"] == SCH
            and doc["dueSlot"] == "2026-11-09T00:00:00Z"
            and doc["deliveredAt"] == "2026-11-09T00:04:00Z"
            and doc["occurrenceKey"] == expect
            and doc["keyUsesDueSlot"] is True
            and doc["keyUsesDeliveredAt"] is False
            and doc["deliveredAt"] != doc["dueSlot"]
        )
    if inv == "INV-PIN":
        if doc["enforcesCheckout"] is not True or len(doc["pins"]) != 2:
            return False
        pull, ref = doc["pins"]
        pull_ok = (
            pull["kind"] == "pull_request"
            and pull["headSha"] == GIT_A
            and pull["baseSha"] == GIT_B
            and pull["checkoutSha"] == pull["headSha"]
            and pull["promptTextSha"] == GIT_C
            and pull["promptTextSha"] != pull["checkoutSha"]
            and len(pull["baseSha"]) == 40
            and len(pull["headSha"]) == 40
        )
        ref_ok = (
            ref["kind"] == "ref"
            and ref["refSha"] == GIT_D
            and ref["checkoutSha"] == ref["refSha"]
            and ref["promptTextSha"] == GIT_C
            and ref["promptTextSha"] != ref["checkoutSha"]
            and len(ref["refSha"]) == 40
        )
        return pull_ok and ref_ok
    if inv == "INV-TREE":
        return (
            doc["synthetic"] is True
            and doc["atumRoster"] is False
            and doc["nativeHierarchy"] is True
            and doc["personas"] == PERSONAS
            and doc["maxDepth"] == 2
            and doc["absoluteMaxDepth"] == 8
            and doc["rootCount"] == 1
            and doc["acyclic"] is True
            and doc["connected"] is True
            and doc["delegation"] == "direct_manager"
            and doc["allowAncestorShortcut"] is False
            and _graph_ok(doc["personas"])
            and doc["note"]
            == "synthetic coordinator/lead/two-specialist tree; not an Atum roster"
        )
    if inv == "INV-DEPTH":
        if doc["maxDepth"] != 2 or doc["absoluteMaxDepth"] != 8:
            return False
        within = doc["depth"] <= doc["maxDepth"]
        if doc["accepted"] is not within:
            return False
        if within:
            return doc["code"] is None
        return doc["code"] == "depth_exceeded"
    if inv == "INV-DELEGATION":
        direct = (
            doc["delegator"] == doc["reportsTo"]
            and doc["delegator"] in doc["spawnableBy"]
            and doc["allowAncestorShortcut"] is False
        )
        if doc["allowed"] is not direct:
            return False
        if direct:
            return doc["code"] is None and doc["target"] == "specialist-a"
        return doc["code"] == "delegation_denied"
    if inv == "INV-PATH":
        safe = _safe_path(doc["path"])
        if doc["published"] is not safe:
            return False
        if safe:
            return doc["code"] is None and doc["path"] == "reports/scan.json"
        return doc["code"] == "result_path_rejected"
    if inv == "INV-SIZE":
        if doc["reportMaxBytes"] != 1048576 or doc["artifactMaxBytes"] != 16777216:
            return False
        fits = 0 <= doc["reportBytes"] <= doc["reportMaxBytes"]
        if doc["published"] is not fits:
            return False
        if fits:
            return doc["code"] is None and doc["reportBytes"] == 1024
        return doc["code"] == "result_oversize"
    if inv == "INV-BODY":
        if doc["maxBytes"] != 8192:
            return False
        return doc["accepted"] is (0 <= doc["bodyByteLength"] <= 8192)
    if inv == "INV-PAGINATION":
        if (
            doc["maxLimit"] != 100
            or doc["defaultLimit"] != 50
            or doc["cursorOpaque"] is not True
        ):
            return False
        if doc["limit"] != 50:
            return False
        return doc["accepted"] is (1 <= doc["limit"] <= doc["maxLimit"])
    if inv == "INV-DROP":
        deliveries = doc["deliveries"]
        if (
            doc["messageId"] != MSG
            or doc["lost"] is not False
            or doc["applyCount"] != 1
        ):
            return False
        if len(deliveries) != 2:
            return False
        first, second = deliveries
        return (
            first["deliveryId"] == DLV
            and second["deliveryId"] == DLV_B
            and first["responseDropped"] is True
            and first["acked"] is False
            and second["acked"] is True
            and second["responseDropped"] is False
        )
    if inv == "INV-LATE-COMMIT":
        local = doc["runLocalSeqs"]
        return (
            doc["events"] == LEDGER_EVENTS
            and _ledger_ok(doc["events"])
            and doc["orderMechanism"] == "invocation_ledger_row_lock"
            and doc["factorySeqIsInvocationOrder"] is False
            and doc["runEventSeqScope"] == "per_run"
            and doc["sseCursor"] == "run_events.seq"
            and local
            == [
                {"runId": RUN, "seq": 1},
                {"runId": RUN_B, "seq": 1},
            ]
        )
    if inv == "INV-CAPACITY":
        return (
            doc["capacity"] == 1
            and doc["steps"] == CAPACITY_STEPS
            and _capacity_ok(doc["steps"], 1)
        )
    if inv == "INV-ROLLUP":
        leaves = _dedup_leaves(doc["leaves"])
        by: dict[str, int] = {}
        for leaf in leaves:
            by[leaf["agent"]] = by.get(leaf["agent"], 0) + leaf["cents"]
        total = sum(by.values())
        lead = by.get("lead", 0) + by.get("specialist-a", 0) + by.get("specialist-b", 0)
        rollups = doc["rollups"]
        return (
            doc["leaves"] == LEAVES
            and total == 800
            and lead == 800
            and rollups["factory"] == total
            and rollups["invocation"] == total
            and rollups["lead"] == lead
            and rollups["leadAgent"] == by["lead"]
            and rollups["specialist-a"] == by["specialist-a"]
            and rollups["specialist-b"] == by["specialist-b"]
            and doc["forbiddenInclusiveSum"] == rollups["invocation"] + rollups["lead"]
            and rollups["factory"] != doc["forbiddenInclusiveSum"]
        )
    if inv == "INV-CONCURRENT":
        committed = [row for row in doc["attempts"] if row["committed"]]
        refused = [row for row in doc["attempts"] if not row["committed"]]
        reserved = sum(row["cents"] for row in committed)
        return (
            doc["ceilingCents"] == 2500
            and doc["partialReserve"] is False
            and len(committed) == 1
            and len(refused) == 1
            and refused[0]["code"] == "budget_exceeded"
            and committed[0]["id"] == NRES
            and refused[0]["id"] == NRES_B
            and reserved <= doc["ceilingCents"]
            and doc["reservedCents"] == reserved
            and doc["spentCents"] == 0
            and reserved + refused[0]["cents"] > doc["ceilingCents"]
        )
    if inv == "INV-ALERT80":
        return (
            doc["thresholdPercent"] == 80
            and doc["ceilingCents"] == 1000
            and doc["charges"] == [900, -200, 200]
            and doc["alerts"] == _alerts(doc["ceilingCents"], doc["charges"])
        )
    if inv == "INV-MISSING-USAGE":
        bound = doc["reservedCents"] if not doc["usageArrived"] else 0
        return (
            doc["deadlinePassed"] is True
            and doc["usageArrived"] is False
            and doc["admitNew"] is False
            and doc["spentCents"] == 0
            and doc["overshootBoundCents"] == bound
            and bound == 300
        )
    if inv == "INV-OVERSHOOT":
        return (
            doc["gatewayCapCents"] <= doc["ceilingCents"]
            and doc["overshootBeyondCeilingCents"] == 0
            and doc["reservedInFlightCents"] == 300
            and doc["ceilingCents"] == 2500
        )
    if inv == "INV-LATE-CHARGE":
        excess = doc["reportedCents"] - doc["reservedCents"]
        return (
            excess == 150
            and doc["quarantinedCents"] == excess
            and doc["spentCents"] == doc["reservedCents"]
            and doc["paused"] is True
            and doc["code"] == "usage_late_over_reservation"
        )
    if inv == "INV-GATEWAY":
        return (
            doc["sharedKey"] is False
            and doc["isolation"] == "per_engagement"
            and doc["gatewayCapCents"] <= doc["engagementCapCents"]
            and doc["gatewayCapLeqCeiling"] is True
            and doc["engagementCapCents"] == 2500
        )
    if inv == "INV-SHARED-KEY":
        return (
            doc["sharedKey"] is True
            and doc["perEngagementAllocation"] is False
            and doc["spawnRefused"] is True
            and doc["guaranteesCeiling"] is False
            and doc["inFlightUncapped"] is True
        )
    if inv == "INV-BUDGET-OVERRIDE":
        return (
            doc["actor"] == "api_key:ops"
            and doc["auditCommitted"] is True
            and doc["accepted"] is True
            and doc["priorCeilingCents"] == 2500
            and doc["ceilingCents"] == 4000
            and doc["ceilingCents"] > doc["priorCeilingCents"]
            and doc["spentDeltaCents"] == 0
            and doc["writes"] == 1
            and doc["code"] is None
        )
    if inv == "INV-SECURITY-GATE":
        witness = (
            doc["backend"] == "docker"
            and doc["g14ActualBackend"] is False
            and doc["g18ActualBackend"] is False
            and doc["gatewayCapProven"] is False
            and doc["helmRenderPassed"] is True
            and doc["kindPassed"] is True
            and doc["hostFirewallEnforced"] is False
            and doc["cniProved"] is False
        )
        proved = (
            doc["g14ActualBackend"] is True
            and doc["g18ActualBackend"] is True
            and doc["gatewayCapProven"] is True
            and (
                (doc["backend"] == "docker" and doc["hostFirewallEnforced"] is True)
                or (doc["backend"] == "k8s" and doc["cniProved"] is True)
            )
        )
        return witness and doc["paidProbeAllowed"] is proved and proved is False
    if inv == "INV-ATTESTATION":
        match = doc["observed"] == doc["bound"]
        required = {
            "adapterDigest",
            "backendId",
            "codeDigest",
            "configDigest",
            "imageDigest",
            "secretPolicy",
        }
        return doc["valid"] is match and set(doc["invalidatesOn"]) == required
    if inv == "INV-TOKEN":
        child = set(doc["childGrants"])
        parent = set(doc["parentGrants"])
        return (
            doc["parentGrants"] == ["ack.write", "inbox.read", "roster.dispatch"]
            and doc["childGrants"] == ["ack.write", "inbox.read"]
            and child <= parent
            and doc["childCanDispatch"] is False
            and doc["specialistDispatch"] is False
            and doc["crossInvocation"] is False
            and "roster.dispatch" not in child
        )
    if inv == "INV-PROFILE":
        selection = doc["selectionAccepted"]
        rejected = doc["injectionRejected"]
        selection_ok = selection["fields"] == ["name"] and selection["accepted"] is True
        reject_ok = (
            rejected["injected"] == ["budgetCeilingCents"]
            and rejected["accepted"] is False
            and rejected["writes"] == 0
            and rejected["code"] == "profile_rejected"
        )
        return selection_ok and reject_ok
    if inv == "INV-IDENTITIES":
        ids = [doc["taskId"], doc["assignee"], doc["activationId"], doc["executionId"]]
        return (
            len(ids) == len(set(ids))
            and doc["taskId"] == NT_A
            and doc["assignee"] == "specialist-a"
            and doc["activationId"] == NACT
            and doc["executionId"] == EXEC
            and doc["blockerIds"] == [NT_B]
        )
    if inv == "INV-PR-DISTINCT":
        return (
            doc["invocationIds"] == [INV, INV_B]
            and doc["sourceOccurrenceKeys"] == ["human:review:1", "human:review:2"]
            and doc["pullRequest"] == "https://github.com/example/atum-core/pull/2213"
            and len(set(doc["invocationIds"])) == 2
        )
    if inv == "INV-CORRELATION":
        return (
            doc["threadRefIsIdentity"] is False
            and doc["invocationIdsDistinct"] is True
            and doc["sameThreadRef"] == "slack:T/C/1555"
            and doc["sourceOccurrenceKeys"] == ["human:review:1", "human:review:2"]
        )
    if inv == "INV-TWO-SLOTS":
        return (
            doc["itemCount"] == 2
            and doc["distinct"] is True
            and doc["priorFinished"] is True
            and doc["slots"] == ["2026-11-02T00:00:00Z", "2026-11-09T00:00:00Z"]
            and doc["workItemIds"] == [TASK, TASK_B]
        )
    if inv == "INV-NO-FINDINGS-ENUM":
        return (
            doc["findingsEnum"] is False
            and doc["artifactsEnum"] is False
            and doc["allowedRequires"] == ["pullRequest", "report"]
        )
    if inv == "INV-REQUIRES-REPORT":
        return (
            doc["requires"] == ["report"]
            and doc["storagePresent"] is True
            and doc["validatorPresent"] is True
        )
    if inv == "INV-SECRET-ALLOWLIST":
        allowed = doc["name"] in doc["allowlist"]
        if doc["resolved"] is not allowed:
            return False
        if allowed:
            return doc["code"] is None
        return doc["code"] == "secret_ref_rejected"
    if inv == "INV-SECRET-UNSAFE":
        return (
            doc["resolved"] is False
            and doc["code"] == "secret_ref_rejected"
            and doc["missingRef"] is True
            and doc["unsafePath"] == "../secrets/token"
        )
    if inv == "INV-READONLY-TOKEN":
        return (
            doc["scannerTask"] is True
            and doc["contents"] == "read"
            and doc["metadata"] == "read"
            and doc["pullRequests"] == "none"
        )
    if inv == "INV-BRIDGE":
        return (
            doc["owners"] == ["factory-outbound-bridge"]
            and doc["enabled"] is False
            and doc["blocksExecution"] is False
            and doc["outageQueues"] == "reporting"
            and doc["echoSuppressed"] is True
            and doc["replaySuppressed"] is True
            and doc["sendAuthorized"] is False
            and doc["sendCount"] == 0
        )
    if inv == "INV-STEER":
        if doc["negotiatedSteer"] is True:
            return doc["delivery"] == "steer" and doc["running"] is True
        return doc["delivery"] == "pending" and doc["running"] is True
    if inv == "INV-COMPLETED-WAKE":
        return (
            doc["sameRun"] is False
            and doc["priorTerminalMutated"] is False
            and doc["priorRunId"] == RUN
            and doc["newRunId"] == RUN_B
            and doc["newRunId"] != doc["priorRunId"]
            and doc["linkedFromRunId"] == doc["priorRunId"]
            and doc["priorTerminal"] == "succeeded"
        )
    if inv == "INV-RETRY-EXEC":
        return (
            doc["sameRun"] is True
            and doc["sameExecution"] is False
            and doc["runId"] == RUN
            and doc["priorExecutionId"] == EXEC
            and doc["newExecutionId"] == EXEC_B
            and doc["newExecutionId"] != doc["priorExecutionId"]
        )
    if inv == "INV-NO-RESURRECT":
        return (
            doc["autoResurrect"] is False
            and doc["newRun"] is False
            and doc["code"] == "cancelled_no_resurrect"
        )
    if inv == "INV-OVERLAP":
        return (
            doc["policy"] == "forbid"
            and doc["priorState"] == "running"
            and doc["slotState"] == "skipped"
            and doc["outcome"] == "skipped_overlap"
            and doc["continuedWorkItemId"] is None
            and doc["countsAsCompletedScan"] is False
            and doc["runStarted"] is False
        )
    if inv == "INV-CONFLICT":
        return (
            doc["sameKey"] is True
            and doc["sameCanonical"] is False
            and doc["httpStatus"] == 409
            and doc["code"] == "invocation_conflict"
            and doc["writes"] == 0
            and doc["originalReceiptUnchanged"] is True
        )
    if inv == "INV-INTENT-CONFLICT":
        return (
            doc["sameKey"] is True
            and doc["intents"] == ["new", "wake"]
            and doc["httpStatus"] == 409
            and doc["code"] == "invocation_conflict"
            and doc["writes"] == 0
        )
    # Remaining worlds are closed equality checks against EXPECT.
    return {k: v for k, v in doc.items() if k != "invariant"} == EXPECT[inv]
