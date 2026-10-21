# shellcheck shell=bash
# T76b cases. spike/m6/run_test.sh sources this file.

t76b_case() {
  bash "$ROOT/spike/m6/t76b_cases.sh" "$@"
}

for t76b_mode in offline live live-ci; do
  run_case "term-during-playwright-${t76b_mode}" t76b_case term-during-playwright "$t76b_mode"
  run_case "int-during-playwright-pid-${t76b_mode}" t76b_case int-during-playwright "$t76b_mode" pid
  run_case "int-during-playwright-group-${t76b_mode}" t76b_case int-during-playwright "$t76b_mode" group
  run_case "specs-no-stack-${t76b_mode}" t76b_case specs-no-stack "$t76b_mode"
  run_case "specs-stale-pid-${t76b_mode}" t76b_case specs-stale-pid "$t76b_mode"
  run_case "default-refuses-${t76b_mode}" t76b_case default-refuses-live-stack "$t76b_mode"
done

for t76b_mode in live live-ci; do
  run_case "int-remote-${t76b_mode}" t76b_case term-during-remote-cleanup "$t76b_mode"
  run_case "cancel-fast-${t76b_mode}" t76b_case cancel-window-budget "$t76b_mode" fast
  run_case "cancel-slow-${t76b_mode}" t76b_case cancel-window-budget "$t76b_mode" slow
  run_case "cancel-watch-${t76b_mode}" t76b_case cancel-window-budget "$t76b_mode" watch
  run_case "exit-unbudgeted-${t76b_mode}" t76b_case exit-path-unbudgeted "$t76b_mode"
  run_case "specs-mode-mismatch-${t76b_mode}" t76b_case specs-mode-mismatch "$t76b_mode"
  run_case "keep-up-records-${t76b_mode}" t76b_case keep-up-records "$t76b_mode"
  run_case "preflight-starts-nothing-${t76b_mode}" t76b_case preflight-starts-nothing "$t76b_mode" \
    "$(if [[ "$t76b_mode" == live ]]; then printf '%s' "missing FACTORY_INFERENCE_OPENAI_COMPATIBLE_BASE_URL"; else printf '%s' "missing M1_OPENAI_BASE_URL"; fi)"
done

run_case specs-fake-stack t76b_case specs-fake-stack
run_case down-twice t76b_case down-twice
run_case stack-env-not-evidence t76b_case stack-env-not-evidence
