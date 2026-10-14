# v1alpha1 JSON Schemas

Layer 1 of definition validation (docs/03). `factoryd` serves these documents
without authentication:

- `GET /api/v1/factory-files/schemas`
- `GET /api/v1/factory-files/schemas/v1alpha1`
- `GET /api/v1/factory-files/schemas/v1alpha1/<document>`

Unknown keys are rejected.

Markdown frontmatter resources (agent.md, automation.md, scorer.md, SKILL.md) are validated offline (layer 2), not by JSON Schema.

YAML resources covered here: `factory.yaml`, `runners/<name>.yaml`, `routers/<name>.yaml`, and `webhooks/<name>.yaml`.
