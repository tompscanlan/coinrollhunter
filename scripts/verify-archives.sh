#!/usr/bin/env bash
# Verify the release archives that scripts/release.sh produced in dist/.
#
# Compiling all six targets proves almost nothing about what a user unzips: the
# archive can still ship a console-subsystem CoinRollHunter.exe (a black flashing
# window on every double-click), a unix binary with no exec bit, or a binary that
# embedded an EMPTY UI (web/dist is a gitignored build artifact resolved against a
# .gitkeep, so a bare `go build` without `make ui` serves nothing and still exits
# 0). None of those fail to compile; all of them fail here.
#
#   ./scripts/verify-archives.sh            # checks ./dist
#   ./scripts/verify-archives.sh some/dir   # checks some/dir
#
# Exits nonzero if any archive is missing, corrupt, or wrong.
set -uo pipefail
cd "$(dirname "$0")/.."

DIST="${1:-dist}"

# The six targets release.sh ships. Unix → tar.gz with one `coinrollhunter`
# binary; windows → zip with a GUI CoinRollHunter.exe, a console
# cli/coinrollhunter.exe, and a READ ME FIRST.txt.
UNIX_TARGETS=(linux_amd64 linux_arm64 darwin_amd64 darwin_arm64)
WIN_TARGETS=(windows_amd64 windows_arm64)

# The mount point in web/dist/index.html. Present in the binary only if the built
# UI was actually embedded; absent for a bare `go build` against the .gitkeep. The
# hashed asset filenames change every build, so we match the stable HTML, not them.
UI_MARKER='<div id="app"></div>'

fails=0
pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; fails=$((fails + 1)); }

# Resolve the single archive for a target suffix (version is in the name and
# varies, so glob rather than hardcode). Echoes the path, or empty if not exactly
# one match.
archive_for() {
  local suffix="$1" ext="$2" matches
  matches=("$DIST"/coinrollhunter_*_"${suffix}.${ext}")
  [ -e "${matches[0]}" ] || { echo ""; return; }
  [ "${#matches[@]}" -eq 1 ] || { echo ""; return; }
  echo "${matches[0]}"
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "==> Verifying archives in $DIST/"

# --- checksums: existence + integrity ------------------------------------------
# sha256sum -c is the load-bearing corruption check: flip one byte in any archive
# and this turns the run red.
if [ ! -f "$DIST/checksums.txt" ]; then
  fail "checksums.txt exists"
else
  pass "checksums.txt exists"
  if ( cd "$DIST" && sha256sum -c checksums.txt >/dev/null 2>&1 ); then
    pass "all archives match their checksums (not corrupt)"
  else
    fail "checksums.txt verification (an archive is missing or corrupt):"
    ( cd "$DIST" && sha256sum -c checksums.txt 2>&1 | grep -v ': OK$' | sed 's/^/       /' )
  fi
fi

# --- every target archive is present and listed in checksums.txt ---------------
for t in "${UNIX_TARGETS[@]}"; do
  a="$(archive_for "$t" tar.gz)"
  if [ -z "$a" ]; then fail "archive present: *_${t}.tar.gz"; else pass "archive present: $(basename "$a")"; fi
done
for t in "${WIN_TARGETS[@]}"; do
  a="$(archive_for "$t" zip)"
  if [ -z "$a" ]; then fail "archive present: *_${t}.zip"; else pass "archive present: $(basename "$a")"; fi
done

if [ -f "$DIST/checksums.txt" ]; then
  n=$(grep -c . "$DIST/checksums.txt")
  if [ "$n" -eq 6 ]; then pass "checksums.txt lists all six archives"; else fail "checksums.txt lists all six archives (found $n)"; fi
fi

# --- unix archives: exec bit + embedded UI -------------------------------------
for t in "${UNIX_TARGETS[@]}"; do
  a="$(archive_for "$t" tar.gz)"
  [ -z "$a" ] && continue
  bin="coinrollhunter_${t}/coinrollhunter"

  # Exec bit as stored IN the archive: tar's own listing, not a post-extract stat
  # (extraction is subject to umask; the tar header is the source of truth).
  perms="$(tar -tvzf "$a" 2>/dev/null | awk -v b="$bin" '$NF==b {print $1}')"
  if [ -z "$perms" ]; then
    fail "$t: archive contains $bin"
  elif [ "${perms:3:1}" = "x" ]; then
    pass "$t: $bin carries the exec bit ($perms)"
  else
    fail "$t: $bin is missing the exec bit ($perms)"
  fi

  # Embedded UI really present in the binary.
  d="$WORK/$t"; mkdir -p "$d"
  if tar -xzf "$a" -C "$d" 2>/dev/null && [ -f "$d/$bin" ]; then
    if grep -a -q -F "$UI_MARKER" "$d/$bin"; then
      pass "$t: embedded UI present (index.html mount point found)"
    else
      fail "$t: embedded UI MISSING (empty web/dist? ran go build without make ui)"
    fi
  else
    fail "$t: could not extract $bin"
  fi
done

# --- windows archives: members + PE subsystem + embedded UI ---------------------
for t in "${WIN_TARGETS[@]}"; do
  a="$(archive_for "$t" zip)"
  [ -z "$a" ] && continue
  root="coinrollhunter_${t}"

  members="$(unzip -Z1 "$a" 2>/dev/null)"
  for want in "$root/CoinRollHunter.exe" "$root/cli/coinrollhunter.exe" "$root/READ ME FIRST.txt"; do
    if grep -qxF "$want" <<<"$members"; then
      pass "$t: archive contains $(basename "$want")"
    else
      fail "$t: archive MISSING $want"
    fi
  done

  d="$WORK/$t"; mkdir -p "$d"
  unzip -qo "$a" -d "$d" 2>/dev/null

  gui="$d/$root/CoinRollHunter.exe"
  cli="$d/$root/cli/coinrollhunter.exe"

  # The whole point of the -H=windowsgui variant: it must actually be the GUI
  # subsystem. A silent regression to console ships a flashing black window.
  if [ -f "$gui" ] && file "$gui" | grep -q 'PE32+ executable (GUI)'; then
    pass "$t: CoinRollHunter.exe is the GUI subsystem"
  else
    fail "$t: CoinRollHunter.exe is NOT GUI subsystem — $( [ -f "$gui" ] && file -b "$gui" || echo missing )"
  fi

  # The CLI copy must stay console, or the subcommands run blind (no std handles).
  if [ -f "$cli" ] && file "$cli" | grep -q 'PE32+ executable (console)'; then
    pass "$t: cli/coinrollhunter.exe is the console subsystem"
  else
    fail "$t: cli/coinrollhunter.exe is NOT console subsystem — $( [ -f "$cli" ] && file -b "$cli" || echo missing )"
  fi

  if [ -f "$gui" ] && grep -a -q -F "$UI_MARKER" "$gui"; then
    pass "$t: embedded UI present in CoinRollHunter.exe"
  else
    fail "$t: embedded UI MISSING in CoinRollHunter.exe"
  fi
done

echo
if [ "$fails" -eq 0 ]; then
  echo "==> OK: all archive checks passed"
  exit 0
else
  echo "==> FAILED: $fails archive check(s) failed"
  exit 1
fi
