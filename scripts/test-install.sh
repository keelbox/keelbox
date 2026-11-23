#!/usr/bin/env bash
# Smoke-test get/install.sh and the installed binary against a local release.
#   scripts/test-install.sh [DIST]
# DIST is a goreleaser dist directory (default dist/release, from
# `make snapshot`). It is served on 127.0.0.1 in the layout of
# get.keelbox.dev plus the GitHub release, then the installer runs with
# /bin/sh --test (which honours the local URLs and test keys): fresh install
# with completion, rerun, the PATH line per shell (and adding it, on a
# terminal), --prefix, up after installing, pinned version, wget instead of curl, and each check refusing what
# it should: the test overrides without --test, tampered binary, SHA256SUMS
# not signed by a trusted key, index and SHA256SUMS disagreeing, an unknown
# release host, no tool to check the signature, a wget redirect off HTTPS. The snapshot is signed with a throwaway
# release key (internal/tools/releasesign testkey), never the real one; a
# DIST that already holds SHA256SUMS.sig and test-keys/ (the macOS CI job,
# which has no Go) is used as is. Finally the installed keelbox runs version,
# init and plan.
# shellcheck disable=SC2016 # PATH lines are checked as printed, with a literal $HOME
set -euo pipefail
cd "$(dirname "$0")/.."
dist="${1:-dist/release}"
version="$(sed -n 's/.*"tag":"\([^"]*\)".*/\1/p' "${dist}/metadata.json")"
[[ -n "${version}" ]] || { echo "no tag in ${dist}/metadata.json" >&2; exit 1; }

work="$(mktemp -d)"
trap 'kill "${server:-}" 2>/dev/null || true; rm -rf "${work}"' EXIT
srv="${work}/srv" rel="${work}/srv/dl/${version}"
mkdir -p "${rel}"
if [[ ! -f "${dist}/SHA256SUMS.sig" ]]; then
  mise exec -- go run ./internal/tools/releasesign testkey "${dist}/test-keys"
  KEELBOX_RELEASE_KEYS="${dist}/test-keys/keys.pem" KEELBOX_RELEASE_SIGNING_KEY="$(cat "${dist}/test-keys/private.pem")" \
    mise exec -- go run ./internal/tools/releasesign sign "${version}" "${dist}/SHA256SUMS"
fi
cp "${dist}/SHA256SUMS" "${dist}/SHA256SUMS.sig" "${dist}/SHA256SUMS.sshsig" "${rel}/"
# A second throwaway key, which the installer must not trust (needs Go).
other="${work}/other-keys"
if command -v mise >/dev/null; then mise exec -- go run ./internal/tools/releasesign testkey "${other}"; fi
for b in "${dist}"/keelbox_*/keelbox; do
  d="$(basename "$(dirname "$b")")" # keelbox_<os>_<arch>_<variant>
  IFS=_ read -r _ os arch _ <<<"$d"
  cp "$b" "${rel}/keelbox-${os}-${arch}"
done

port="$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
base="http://127.0.0.1:${port}"
scripts/channel-index.sh "${version}" "${rel}/SHA256SUMS" "${base}/dl" >"${srv}/stable.json"
python3 -m http.server --bind 127.0.0.1 --directory "${srv}" "${port}" >/dev/null 2>&1 &
server=$!
for _ in $(seq 50); do curl -fs "${base}/stable.json" >/dev/null && break; sleep 0.1; done

# A snapshot is unsigned: keep cosign (if mise put it on PATH) out of reach.
export PATH=/usr/bin:/bin
export KEELBOX_INDEX_URL="${base}/stable.json" KEELBOX_RELEASE_URL="${base}/dl"
export KEELBOX_TEST_RELEASE_KEYS="${dist}/test-keys"
bin="${work}/bin" home="${work}/home"
mkdir -p "${home}"
# Every run writes completion and may print rc lines under HOME: never the real one.
export HOME="${home}"
unset XDG_DATA_HOME XDG_CONFIG_HOME ZDOTDIR
step() { printf '\n== %s\n' "$*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

step "fresh install"
HOME="${home}" /bin/sh get/install.sh --test | tee "${work}/out"
[[ -x "${home}/.local/bin/keelbox" ]] || fail "not installed to ~/.local/bin"
grep -q "is not on your PATH" "${work}/out" || fail "no PATH hint"
grep -q "Release key signature verified" "${work}/out" || fail "release key signature not reported"
if grep -qi "cosign\|sigstore" "${work}/out"; then fail "an install without cosign mentions it"; fi
grep -q "Checksum matches SHA256SUMS and" "${work}/out" || fail "index cross-check not reported"
if grep -q "once a day" "${work}/out"; then fail "the update-check notice printed during the install"; fi

# refused DIR WANT [ENV...]: the installer must fail with WANT and install nothing.
refused() {
  local d="$1" want="$2"
  shift 2
  if env "$@" /bin/sh get/install.sh --test --dir "${work}/${d}" 2>"${work}/err" >/dev/null; then fail "${d}: installed"; fi
  grep -q "${want}" "${work}/err" || fail "${d}: unexpected error: $(cat "${work}/err")"
  [[ ! -e "${work}/${d}/keelbox" ]] || fail "${d}: binary left in place"
  echo "refused: $(cat "${work}/err")"
}

grep -q "Remove: rm ~/.local/bin/keelbox" "${work}/out" || fail "no uninstall line"
[[ -s "${home}/.local/share/bash-completion/completions/keelbox" ]] || fail "no bash completion"
grep -q "complete -F _keelbox keelbox" "${home}/.local/share/bash-completion/completions/keelbox" || fail "bash completion is not keelbox's"
# The installer itself is in the signed SHA256SUMS.
listed="$(awk '$2 == "install.sh" { print $1 }' "${rel}/SHA256SUMS")"
actual="$({ sha256sum get/install.sh 2>/dev/null || shasum -a 256 get/install.sh; } | cut -d' ' -f1)"
[[ -n "${listed}" && "${listed}" == "${actual}" ]] || fail "SHA256SUMS does not list get/install.sh (${listed:-none}, want ${actual})"

step "rerun is a no-op"
HOME="${home}" /bin/sh get/install.sh --test | tee "${work}/out"
grep -q "already installed" "${work}/out" || fail "rerun reinstalled"

step "the agent skill, where Claude Code is set up"
mkdir -p "${home}/.claude"
HOME="${home}" /bin/sh get/install.sh --test --no-modify-path | tee "${work}/out"
grep -qF "keelbox agent-skill install" "${work}/out" || fail "no agent-skill line with ~/.claude"
[[ ! -e "${home}/.claude/skills/keelbox" ]] || fail "the skill was added without a yes"
rmdir "${home}/.claude"

step "the PATH line for the user's shell"
HOME="${home}" SHELL=/bin/zsh /bin/sh get/install.sh --test | tee "${work}/out"
grep -qF 'export PATH="$HOME/.local/bin:$PATH"' "${work}/out" || fail "no zsh PATH line"
grep -qF "Add this line to ~/.zshrc" "${work}/out" || fail "not ~/.zshrc for zsh"
HOME="${home}" SHELL=/usr/bin/fish /bin/sh get/install.sh --test | tee "${work}/out"
grep -qF 'fish_add_path $HOME/.local/bin' "${work}/out" || fail "no fish PATH line"
printf '\nexport PATH="$HOME/.local/bin:$PATH"\n' >>"${home}/.zshrc"
HOME="${home}" SHELL=/bin/zsh /bin/sh get/install.sh --test | tee "${work}/out"
grep -qF "zshrc puts ~/.local/bin on PATH. Open a new terminal" "${work}/out" || fail "line already in ~/.zshrc not seen"
if script --version >/dev/null 2>&1; then
  # On a terminal it offers to add the line, and does.
  rm -f "${home}/.bashrc"
  printf 'y\n' | HOME="${home}" SHELL=/bin/bash script -qec "/bin/sh get/install.sh --test" /dev/null >"${work}/out"
    grep -qF 'export PATH="$HOME/.local/bin:$PATH"' "${home}/.bashrc" || fail "line not added to ~/.bashrc: $(cat "${work}/out")"
  grep -q "Added to ~/.bashrc" "${work}/out" || fail "adding not reported"
fi

step "--prefix: binary in PREFIX/bin, completion under PREFIX/share"
HOME="${home}" /bin/sh get/install.sh --test --prefix "${work}/prefix" | tee "${work}/out"
[[ -x "${work}/prefix/bin/keelbox" ]] || fail "not installed to PREFIX/bin"
[[ -s "${work}/prefix/share/bash-completion/completions/keelbox" ]] || fail "no bash completion under PREFIX"
if command -v zsh >/dev/null; then [[ -s "${work}/prefix/share/zsh/site-functions/_keelbox" ]] || fail "no zsh completion under PREFIX"; fi
if /bin/sh get/install.sh --test --prefix "${work}/p2" --dir "${work}/d2" 2>"${work}/err"; then fail "--prefix with --dir accepted"; fi
grep -q "give one" "${work}/err" || fail "unexpected error: $(cat "${work}/err")"

step "up: keelbox up runs once installed, with the flags after it"
HOME="${home}" /bin/sh get/install.sh --test up --help | tee "${work}/out"
grep -q "Starting keelbox up" "${work}/out" || fail "up not started"
grep -q "keelbox up \[box\]: set up a box" "${work}/out" || fail "up --help not passed through"

step "pinned version, custom dir"
/bin/sh get/install.sh --test --version "${version#v}" --dir "${bin}"
"${bin}/keelbox" version | grep -q "^keelbox ${version} " || fail "wrong version installed"

step "without --test the overrides are refused"
# no_test WANT [ENV...]: the installer, run without --test, must fail with WANT.
no_test() {
  local want="$1"
  shift
  if env "$@" /bin/sh get/install.sh --dir "${work}/no-test" 2>"${work}/err" >/dev/null; then fail "installed without --test: $*"; fi
  grep -q "${want}" "${work}/err" || fail "unexpected error: $(cat "${work}/err")"
  [[ ! -e "${work}/no-test/keelbox" ]] || fail "installed without --test: $*"
  echo "refused: $(cat "${work}/err")"
}
https_index=KEELBOX_INDEX_URL=https://get.keelbox.dev/stable.json
no_test "KEELBOX_RELEASE_URL is honoured only with --test" "${https_index}"
no_test "KEELBOX_TEST_RELEASE_KEYS is honoured only with --test" -u KEELBOX_RELEASE_URL "${https_index}"
no_test "KEELBOX_INDEX_URL must be an https:// URL" -u KEELBOX_RELEASE_URL -u KEELBOX_TEST_RELEASE_KEYS

step "wget instead of curl"
mkdir -p "${work}/nocurl"
for p in /usr/bin/* /bin/*; do
  t="${work}/nocurl/$(basename "${p}")"
  [[ "$(basename "${p}")" == curl || -e "${t}" || -L "${t}" ]] || ln -s "${p}" "${t}"
done
if [[ -e "${work}/nocurl/wget" ]]; then
  PATH="${work}/nocurl" /bin/sh get/install.sh --test --dir "${work}/wget" | tee "${work}/out"
  [[ -x "${work}/wget/keelbox" ]] || fail "not installed with wget"
else
  echo "skipped: no wget here"
fi
# A wget that follows an HTTPS URL to plain HTTP must be stopped; one that
# cannot be told not to follow redirects (BusyBox) is not used at all.
mkdir -p "${work}/fakewget" "${work}/busybox"
cat >"${work}/fakewget/wget" <<'SH'
#!/bin/sh
case "$*" in *--help*) echo "  --max-redirect=NUM" ;; *) echo "  Location: http://127.0.0.1:1/stable.json" >&2; exit 8 ;; esac
SH
printf '#!/bin/sh\necho "BusyBox v1.36 wget"\nexit 1\n' >"${work}/busybox/wget"
chmod +x "${work}/fakewget/wget" "${work}/busybox/wget"
refused wget-redirect "refusing redirect from https://get.invalid/stable.json to http://127.0.0.1:1/stable.json: not HTTPS" \
  KEELBOX_INDEX_URL=https://get.invalid/stable.json PATH="${work}/fakewget:${work}/nocurl"
refused busybox-wget "install curl or GNU wget" PATH="${work}/busybox:${work}/nocurl"

step "openssl without Ed25519 (macOS LibreSSL): ssh-keygen checks the signature"
mkdir -p "${work}/libressl"
printf '#!/bin/sh\necho "LibreSSL 3.3.6"\n' >"${work}/libressl/openssl"
chmod +x "${work}/libressl/openssl"
PATH="${work}/libressl:${PATH}" /bin/sh get/install.sh --test --dir "${work}/ssh" | tee "${work}/out"
grep -q "Release key signature verified (ssh-keygen)" "${work}/out" || fail "ssh-keygen fallback not used"

step "no tool to check the signature: refused"
mkdir -p "${work}/minimal"
for tool in sh curl mktemp rm sed head grep awk uname sha256sum shasum cut chmod mkdir cp mv cat sysctl; do
  p="$(command -v "${tool}" || true)"
  [[ -n "${p}" ]] && ln -s "${p}" "${work}/minimal/${tool}"
done
refused no-verifier "cannot check the release signature" PATH="${work}/minimal"

step "SHA256SUMS not signed by a trusted key: refused"
if [[ -d "${other}" ]]; then
  refused untrusted "not signed by a keelbox release key" KEELBOX_TEST_RELEASE_KEYS="${other}"
  refused untrusted-ssh "not signed by a keelbox release key" KEELBOX_TEST_RELEASE_KEYS="${other}" PATH="${work}/libressl:${PATH}"
fi
cp "${rel}/SHA256SUMS" "${work}/sums.good"
printf '%s  keelbox-extra\n' "$(printf %064d 0)" >>"${rel}/SHA256SUMS"
refused tampered-sums "not signed by a keelbox release key"
cp "${work}/sums.good" "${rel}/SHA256SUMS"

step "index and SHA256SUMS disagree: refused"
cp "${srv}/stable.json" "${work}/index.good"
sed -i.bak "s/\"${version}\/\([a-z]*-[a-z0-9]*\)\": \"[0-9a-f]*\"/\"${version}\/\1\": \"$(printf %064d 0)\"/" "${srv}/stable.json"
refused disagree "disagree"
cp "${work}/index.good" "${srv}/stable.json"

step "index names a release host keelbox does not know: refused"
refused unknown-host "not one keelbox downloads from" KEELBOX_RELEASE_URL=

step "tampered binary is refused"
host="keelbox-$(uname -s | tr '[:upper:]' '[:lower:]')-$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')"
cp "${rel}/${host}" "${work}/good"
printf 'tampered' >>"${rel}/${host}"
if /bin/sh get/install.sh --test --dir "${work}/bad" 2>"${work}/err"; then fail "tampered binary installed"; fi
grep -q "checksum mismatch" "${work}/err" || fail "unexpected error: $(cat "${work}/err")"
[[ ! -e "${work}/bad/keelbox" ]] || fail "tampered binary left in place"
cp "${work}/good" "${rel}/${host}"

step "a cosign that does not run is skipped and reported, not taken for a bad signature"
mkdir -p "${work}/broken"
printf '#!/bin/sh\necho "No version is set for shim: cosign" >&2\nexit 126\n' >"${work}/broken/cosign"
chmod +x "${work}/broken/cosign"
PATH="${work}/broken:${PATH}" /bin/sh get/install.sh --test --dir "${work}/shim" | tee "${work}/out"
grep -q "cosign is on PATH but does not run: the Sigstore signature was not checked" "${work}/out" || fail "no warning for a cosign that does not run"
[[ -x "${work}/shim/keelbox" ]] || fail "not installed with a broken cosign on PATH"

step "keelbox version, init, plan"
cd "${work}"
"${bin}/keelbox" version
"${bin}/keelbox" init acme.yaml --box acme
# The example targets this machine, and plan needs root: keelbox runs it
# again with sudo. A stand-in sudo that asks for a password (exit 1 under
# -n) checks the command it would run, and nothing runs as root here.
mkdir -p "${work}/nosudo"
printf '#!/bin/sh\necho "$*" >>"%s/sudo-calls"\nexit 1\n' "${work}" >"${work}/nosudo/sudo"
chmod +x "${work}/nosudo/sudo"
set +e
PATH="${work}/nosudo:${PATH}" "${bin}/keelbox" plan acme.yaml 2>err </dev/null
code=$?
set -e
if [[ ${code} -ne 1 ]] || ! grep -q "plan needs root, and sudo asks for a password; in a terminal run: keelbox plan" err; then fail "plan: exit ${code}: $(cat err)"; fi
grep -q "^-n env KEELBOX_CHILD=1 .*/keelbox plan acme.yaml$" "${work}/sudo-calls" || fail "plan did not run itself with sudo: $(cat "${work}/sudo-calls")"
echo "plan runs itself with sudo -n, and says what to do when sudo needs a password"
step "ok"
