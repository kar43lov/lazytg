#!/usr/bin/env bash
# Refuse to let Telegram API credentials enter the repository.
#
# Why this is a hard gate rather than a lint: Telegram permanently rejects an
# api_id once it has been observed in public source (API_ID_PUBLISHED_FLOOD).
# Release binaries carry the credentials injected from CI secrets, so a single
# leaked commit would break login for every user of every shipped release at
# once, and the key cannot be un-published.
#
# Usage:
#   scripts/secret-scan.sh                        # scan all tracked files
#   scripts/secret-scan.sh --staged               # staged additions only (hook)
#   scripts/secret-scan.sh --range A...HEAD       # lines added anywhere in a range
#   scripts/secret-scan.sh --self-test            # verify the matching rules
#
# An api_hash is 32 lowercase hex characters. That shape is what we match; the
# api_id alone is a bare integer, indistinguishable from any other number and
# useless without its hash, so it is not scanned for.
#
# What this gate does NOT catch, stated so nobody mistakes it for airtight:
#   - a key deliberately obfuscated (split across concatenated literals, base64,
#     reversed). A shape-matching grep cannot beat an author who is trying to
#     get around it; this guards against the accidental paste, which is the
#     realistic failure.
#   - a run of hex longer than 32 that merely *contains* a key ("cafebabe"
#     glued to a hash). Treating any long run as a hit would flag every sha1
#     and sha256 digest in the tree instead.
#   - uppercase hex. Telegram issues api_hash in lowercase.
#   - a key inside a binary file. `grep -I` skips those, and a binary diff has
#     no "+" lines to scan. Committing a built binary (which carries the
#     release key by design) is the realistic version of this.
#   - a key already on main before this gate existed. --range covers a branch,
#     --all covers the tree; neither rewrites what is already published.
#   - a PR that edits this script or the CI job that runs it — CI evaluates the
#     weakened version. The diff is visible to a reviewer, which is the actual
#     control here; CODEOWNERS covers scripts/ for the same reason.
set -euo pipefail

# Documentation placeholder. It is deliberately the one 32-hex value allowed
# to live in the tree — README, docs/INSTALL.md and the redaction tests all
# use it, and it is not a real credential.
readonly PLACEHOLDER='0123456789abcdef0123456789abcdef'

# Match hex runs of 32 *or more* and filter to exactly-32 afterwards, rather
# than anchoring the pattern with neighbouring non-hex classes. Those classes
# are consumed by the match, so `grep -o` cannot start the next match at the
# character it just ate: in "<placeholder> <realhash>" only the placeholder
# was ever reported, the allowlist then dropped it, and the real credential
# sailed through the gate. Matching {32,} keeps runs adjacent-safe, and the
# length filter below still rejects sha1 (40) and sha256 (64) digests.
readonly HEXRUN='[0-9a-f]{32,}'

mode="${1:---all}"

workdir=""
# The explicit `return 0` matters: a trap on EXIT whose last command fails
# overrides the script's exit status. Without it `[[ -n "" ]]` returned 1
# whenever no temp dir had been created, so --self-test printed "passed" and
# still exited 1 — a permanently red CI step reporting success in its own log.
cleanup() {
  if [[ -n "$workdir" ]]; then
    rm -rf "$workdir"
  fi
  return 0
}
trap cleanup EXIT

# die aborts with a distinct exit code. A gate that cannot run must never look
# like a gate that ran and found nothing: both print little and exit quietly
# unless the failure is made loud here.
die() {
  echo "secret-scan: $1" >&2
  exit 3
}

# filter_leaks reads "<prefix>:<hexrun>" lines and prints the ones that are a
# real credential shape. Split out of the main flow so --self-test can drive
# the exact code path the hooks use. `\r?$` keeps CRLF files working: GNU grep
# leaves the carriage return on the line, which would otherwise defeat the
# end-anchor on a Windows-authored file.
filter_leaks() {
  grep -E ':[0-9a-f]{32}\r?$' | grep -v "$PLACEHOLDER" || true
}

collect_all() {
  # The file list goes through a temp file rather than a pipe so git's exit
  # code is checkable. Piping it into xargs hid failure completely: outside a
  # repository this printed "clean" and exited 0, i.e. the gate reported
  # success without having scanned a single byte.
  local list="$workdir/files"
  git ls-files -z > "$list" || die "git ls-files failed — cannot scan"
  [[ -s "$list" ]] || die "git ls-files returned nothing — not a repository, or an empty checkout"

  # -I skips binary files. grep exits 1 when nothing matches, which is the
  # normal clean case, so its status is deliberately discarded here — unlike
  # git's above, which is a real failure signal.
  xargs -0 grep -IEon "$HEXRUN" -- < "$list" 2>/dev/null || true
}

collect_range() {
  # Scans every line ADDED anywhere in a commit range, not just the end state.
  # Without this the gate misses its most likely real failure: a contributor
  # commits a key, notices, deletes the file in a follow-up commit, and the
  # branch tip is clean — while the key is still in the history that lands on
  # main, which is exactly what Telegram treats as published.
  local range="$1"
  git rev-parse --verify --quiet "${range%%...*}" > /dev/null ||
    die "cannot resolve '$range' — is the base ref fetched? (actions/checkout needs fetch-depth: 0)"

  git log -p -U0 --no-color "$range" 2>/dev/null |
    grep -E '^\+' | grep -v '^+++' |
    grep -Eon "$HEXRUN" || true
}

collect_staged() {
  # Added lines only: an existing hit elsewhere in a touched file is not this
  # commit's problem, and flagging it would train people to use --no-verify.
  #
  # Scanned per file rather than over one concatenated diff so the report says
  # which file leaked. The flat version printed a line number counted through
  # the whole diff, pointing at nothing — and a hook that says "12:<hex>" with
  # no filename is a hook people bypass.
  local names="$workdir/staged-names"
  git diff --cached --name-only -z --diff-filter=ACMR > "$names" ||
    die "git diff --cached failed — cannot scan"

  local file
  while IFS= read -r -d '' file; do
    git diff --cached -U0 --diff-filter=ACMR -- "$file" |
      grep -E '^\+' | grep -v '^+++' |
      grep -Eon "$HEXRUN" |
      sed "s|^|$file:|" || true
  done < "$names"
}

# run_self_test exercises the matching rules against fixtures with known
# answers. It exists because this gate has already leaked once: the original
# pattern anchored on neighbouring non-hex characters, which made `grep -o`
# skip every hex run after the first on a line — so a placeholder followed by
# a real key reported only the placeholder, and the allowlist then discarded
# it. Case 1 below is that exact regression.
run_self_test() {
  # Fixtures are assembled from two 16-char halves. Written out in full they
  # would be 32-hex runs in a tracked file, and `--all` would flag this very
  # script — a scanner that trips on its own test data is a scanner people
  # disable. The halves are concatenated at runtime, so the shape exists only
  # in memory.
  local key_a key_b
  key_a="aaaabbbbccccdddd""eeeeffff00001111"
  key_b="1111222233334444""5555666677778888"

  local failures=0
  check() {
    local name="$1" input="$2" want="$3" got
    # `|| true` on the pipeline: grep exits 1 when a fixture legitimately has
    # no match, and pipefail would otherwise abort the whole run under set -e.
    got="$(printf '%s\n' "$input" | grep -Eon "$HEXRUN" | filter_leaks | sed 's/^[0-9]*://' | tr '\n' ' ' || true)"
    got="${got% }"
    if [[ "$got" != "$want" ]]; then
      echo "self-test FAIL: $name" >&2
      echo "  want: '$want'" >&2
      echo "  got:  '$got'" >&2
      failures=$((failures + 1))
    fi
  }

  # The regression that prompted this self-test: the placeholder used to mask
  # every key after it on the same line.
  check "placeholder then real key on one line" \
    "doc $PLACEHOLDER real $key_a" \
    "$key_a"
  check "two real keys on one line" \
    "a $key_a b $key_b" \
    "$key_a $key_b"
  check "placeholder alone is allowed" \
    "export LAZYTG_API_HASH=$PLACEHOLDER" \
    ""
  check "sha256 digest is not a credential" \
    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" \
    ""
  check "sha1 digest is not a credential" \
    "da39a3ee5e6b4b0d3255bfef95601890afd80709" \
    ""
  check "uppercase hex is not an api_hash" \
    "AAAABBBBCCCCDDDDEEEEFFFF00001111" \
    ""
  check "bare key with no surrounding text" \
    "$key_a" \
    "$key_a"
  # CRLF: GNU grep keeps the \r on the line, so the end-anchor must tolerate
  # it. Without \r? this file type is a silent blind spot on Linux CI.
  check "CRLF line ending does not hide a key" \
    "$(printf 'api_hash=%s\r' "$key_a")" \
    "$key_a"
  # Known limits, pinned so a future "improvement" has to argue with a failing
  # test rather than quietly change the trade-off. Both alternatives are worse:
  # matching inside longer runs flags every sha1/sha256 in the tree, and
  # case-insensitive matching flags uppercase digests just the same.
  check "known limit: key glued to other hex is not caught" \
    "const k = \"cafebabe$key_a\"" \
    ""
  check "known limit: uppercase hex is not caught" \
    "AAAABBBBCCCCDDDDEEEEFFFF00001111" \
    ""

  if (( failures > 0 )); then
    echo "secret-scan: self-test failed ($failures case(s))" >&2
    exit 1
  fi
  echo "secret-scan: self-test passed"
  exit 0
}

case "$mode" in
  --self-test) run_self_test ;;
  --range)
    [[ $# -ge 2 ]] || die "--range needs a commit range, e.g. --range origin/main...HEAD"
    workdir="$(mktemp -d)" || die "cannot create a temp directory"
    hits="$(collect_range "$2")"
    ;;
  --staged|--all)
    workdir="$(mktemp -d)" || die "cannot create a temp directory"
    if [[ "$mode" == "--staged" ]]; then
      hits="$(collect_staged)"
    else
      hits="$(collect_all)"
    fi
    ;;
  *) echo "usage: $0 [--all|--staged|--range <base>...HEAD|--self-test]" >&2; exit 2 ;;
esac

leaks="$(printf '%s\n' "$hits" | filter_leaks)"

if [[ -n "$leaks" ]]; then
  cat >&2 <<EOF
secret-scan: possible Telegram api_hash found (${mode#--}).

$leaks

An api_hash is 32 hex characters. If this is a real credential, remove it —
Telegram blocks a published api_id permanently and every release user loses
login at once. Credentials belong in:
  - your shell (LAZYTG_API_ID / LAZYTG_API_HASH), for local use
  - repository secrets (LAZYTG_RELEASE_API_ID / LAZYTG_RELEASE_API_HASH),
    for release builds

If this is a false positive (some other 32-hex constant), add it to the
allowlist in scripts/secret-scan.sh.
EOF
  exit 1
fi

echo "secret-scan: clean (${mode#--})"
