#!/usr/bin/env bash
# Offline proof for D6c. CI's shell job runs this with MODE=offline.
# Rehearsals run when FACTORY_TEST_DATABASE_URL and FACTORY_TEST_S3_ENDPOINT
# are set (the go test job, and a local VM). The matrix itself does not
# need them.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${root}"

if [[ "${MODE:-offline}" != "offline" ]]; then
  echo "run_test.sh is offline only" >&2
  exit 1
fi

go test -count=1 -timeout 10m ./deploy/dreaming-live/ -run 'TestOfflineMatrix|TestWorstCaseExample|TestDetailRedactsNeedles'

if [[ -n "${FACTORY_TEST_DATABASE_URL:-}" && -n "${FACTORY_TEST_S3_ENDPOINT:-}" ]]; then
  go test -count=1 -timeout 20m ./deploy/dreaming-live/ -run 'TestRehearsal'
  go test -count=1 -timeout 30m ./internal/server \
    -run '^(TestProducedHeadReviewerPipeline|TestDraftPublishRequiresReviewerPass|TestRunSpendHardStop|TestHarnessSecretsNotInProc|TestPublisherRouteAllowlist|TestPublishGitRejectsBearer|TestGitHTTPAuthHeader)$'
fi

echo "dreaming offline PASS"
