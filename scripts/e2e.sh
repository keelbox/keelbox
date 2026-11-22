#!/usr/bin/env bash
# Run the end-to-end test against a fresh machine.
#   scripts/e2e.sh            local LXD VM (default)
#   scripts/e2e.sh hetzner    Hetzner Cloud (needs ~/.config/keelbox/hetzner.token, mode 600)
#   scripts/e2e.sh aws        Amazon Lightsail (needs the AWS CLI, signed in; KEELBOX_E2E_AWS_PROFILE
#                             and KEELBOX_E2E_AWS_REGION choose the profile and region)
#   scripts/e2e.sh digitalocean  DigitalOcean (needs doctl, signed in; KEELBOX_E2E_DO_CONTEXT and
#                             KEELBOX_E2E_DO_REGION choose the doctl context and region)
#   scripts/e2e.sh gcp        Google Cloud (needs gcloud, signed in; KEELBOX_E2E_GCP_PROJECT and
#                             KEELBOX_E2E_GCP_ZONE choose the project and zone)
# The transcript is written to e2e-logs/<target>-<UTC time>.log (git-ignored).
#
# KEELBOX_E2E_JOBS=N (lxd only, default 1) runs N tests at a time, each as
# its own process with its own VM name prefix, so no test's sweep deletes
# another's VM. Each VM takes 2 vCPU and 2 GiB and some tests use two, so
# allow about 5 GiB of free memory per job. Logs go to
# e2e-logs/<target>-<UTC time>/<test>.log.
set -euo pipefail
cd "$(dirname "$0")/.."
target="${1:-lxd}"
jobs="${KEELBOX_E2E_JOBS:-1}"
mkdir -p e2e-logs
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
export KEELBOX_E2E_TARGET="${target}"

if [ "${target}" != lxd ] || [ "${jobs}" -le 1 ]; then
	log="e2e-logs/${target}-${stamp}.log"
	export KEELBOX_E2E_LOG="${PWD}/${log}"
	echo "e2e: target ${target}, transcript ${log}"
	mise exec -- go test -tags e2e -count=1 -v -timeout 180m ./test/e2e/ 2>&1 | tee "${log}.gotest"
	exit
fi

dir="${PWD}/e2e-logs/${target}-${stamp}"
mkdir -p "${dir}"
bin="${dir}/e2e.test"
mise exec -- go test -tags e2e -c -o "${bin}" ./test/e2e/
# The tests build keelbox from ../../cmd/keelbox, so they run in test/e2e.
cd test/e2e
tests="$("${bin}" -test.list '^Test')"
echo "e2e: target ${target}, ${jobs} at a time, logs in e2e-logs/${target}-${stamp}/"

run_one() {
	local name="$1" word start rc=0
	# kb-<word>-: the test name in lower case without "Test".
	word="$(printf '%s' "${name#Test}" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9')"
	start="$(date +%s)"
	KEELBOX_E2E_VM_PREFIX="kb-${word}-" KEELBOX_E2E_LOG="${dir}/${name}.transcript" \
		mise exec -- "${bin}" -test.run "^${name}\$" -test.v -test.count=1 -test.timeout 120m \
		>"${dir}/${name}.log" 2>&1 || rc=$?
	local result=PASS
	if [ "${rc}" -ne 0 ]; then
		result=FAIL
	elif grep -q "^--- SKIP: ${name} " "${dir}/${name}.log"; then
		result=SKIP
	fi
	printf '%s %s %ss\n' "${result}" "${name}" "$(($(date +%s) - start))" | tee -a "${dir}/summary"
}
export -f run_one
export dir bin

: >"${dir}/summary"
# shellcheck disable=SC2016 # $1 is for the inner bash.
printf '%s\n' "${tests}" | xargs -P "${jobs}" -I{} bash -c 'run_one "$1"' _ {}
echo "e2e: $(grep -c '^PASS' "${dir}/summary" || true) passed, $(grep -c '^SKIP' "${dir}/summary" || true) skipped, $(grep -c '^FAIL' "${dir}/summary" || true) failed"
! grep -q '^FAIL' "${dir}/summary"
