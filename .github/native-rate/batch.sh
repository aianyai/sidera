#!/usr/bin/env bash
# One fixed Go batch; workflow conditions alone decide whether another runs.
set -euo pipefail
family=$1
count=$2
schedule=$3
case "$family" in
  management) name=TestBinaryManagementFeedbackRepro; tags=acceptance,diagnostic ;;
  delivery) name=TestRegisterReplyExactTargetRepro; tags=acceptance,diagnostic ;;
  acceptance) name=TestUnobservedPreparationLeavesGenerationOneForDaemon; tags=acceptance,crashtest,testclock ;;
  maintenance) name=TestManagementMaintenanceExactTargetRepro; tags=acceptance,diagnostic ;;
  *) exit 2 ;;
esac
case "$count" in 10|20) ;; *) exit 2 ;; esac
case "$schedule" in
  default) unset GOMAXPROCS; prefix='' ;;
  1|2) export GOMAXPROCS="$schedule"; prefix="GOMAXPROCS=$schedule " ;;
  *) exit 2 ;;
esac
export SIDERA_CI_DIAGNOSTIC_COMMAND="${prefix}go test -json -race=false -count=$count -v -timeout 90s -tags $tags -run '^$name\$' ./cmd/sidera/internal/acceptance"
set +e
go test -json -race=false -count="$count" -v -timeout 90s -tags "$tags" -run "^$name\$" ./cmd/sidera/internal/acceptance 2>&1 | .scratch/native-rate.exe -family "$family" -count "$count"
codes=("${PIPESTATUS[@]}")
set -e
printf 'NATIVE command_exit=%d classifier_exit=%d schedule=%s\n' "${codes[0]}" "${codes[1]}" "$schedule"
# The original nonzero test status remains the step and job result.
if (( codes[0] != 0 )); then exit "${codes[0]}"; fi
exit "${codes[1]}"
