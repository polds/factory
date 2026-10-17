---
description: Carries out the plan.
agentType: IMPLEMENT
model: m5-building
idleTimeoutMinutes: 1
---

# Implement

Carry out the work item. Do not open a pull request.
Before the summary, narrate the work in several paragraphs so a person can interrupt.
When a follow-up arrives, reply with a one-line summary and no tool calls.
When the title says the agent should report failure, reply with only this JSON object and no other text: {"status":"failed_by_agent","summary":"m5 agent failure"}.
When the title says to wait for the container to stop, keep talking and do not finish.
Do not print tokens or secret values.
