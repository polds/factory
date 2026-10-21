---
name: cite-sources
description: Did the final message cite the required source line?
agents:
  - implementer
output: classification
labels:
  - value: followed
    description: The transcript contains the required source line.
    score: 1
  - value: missed
    description: The transcript does not contain the required source line.
    score: 0
passingScore: 1
samplingRate: 100
selfImprovement: true
---

Return followed only when the transcript contains the exact line CONVENTION: cite-sources.
Return missed when that line is absent.
An undeclared label is invalid.
