# Goal: DB-backed tenant-isolation tests

This template is the task text. It is not a grant, a secret, or a copy of the issue body.

Viktor Halas is the implementer. Oskar Lindgren is the reviewer and does not call a model.

Write DB-backed tenant-isolation tests in the file named by `FACTORY_EVIDENCE_MUTATION`.
The package under test is the directory named by `FACTORY_EVIDENCE_PACKAGE`.

The first occurrence of the literal `factoryEvidencePass` must sit inside a tenant-isolation assertion.
Replacing that literal with `factoryEvidenceFail` must make a named test fail.
The replacement must not be a compile error.

Commit the result locally. Do not push. Do not open a pull request.
The publisher opens one draft pull request after Oskar's evidence on that commit.
