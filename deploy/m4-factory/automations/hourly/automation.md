---
enabled: false
agent: triage
displayName: Hourly schedule
triggers:
  - provider: schedule
    event: cron_fired
    schedule:
      name: hourly
      cron: "@every 1h"
---

Disabled by default. Offline enables this copy only to prove the schedule
loads. Do not print tokens or API keys.
