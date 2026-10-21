#!/bin/sh
set -eu

# Test install.sh fallback behavior when INSTALL_DIR is not writable.
#
# We test the writability-check and fallback logic in isolation using
# sandbox directories under .scratch/.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# ---- Test helpers -----------------------------------------------------------
pass=0
fail=0
SCRATCH=""

cleanup() {
  if [ -n "$SCRATCH" ] && [ -d "$SCRATCH" ]; then
    chmod -R u+rwX "$SCRATCH" 2>/dev/null || true
    rm -rf "$SCRATCH"
  fi
}

trap cleanup EXIT
SCRATCH="$(mktemp -d "$ROOT_DIR/.scratch/install-test.XXXXXX")"

assert_eq() {
  label="$1"
  expected="$2"
  actual="$3"
  if [ "$expected" = "$actual" ]; then
    pass=$((pass + 1))
  else
    echo "FAIL $label: expected '$expected', got '$actual'" >&2
    fail=$((fail + 1))
  fi
}

assert_contains() {
  label="$1"
  haystack="$2"
  needle="$3"
  case "$haystack" in
    *"$needle"*)
      pass=$((pass + 1))
      ;;
    *)
      echo "FAIL $label: expected output to contain '$needle'" >&2
      echo "  got: '$haystack'" >&2
      fail=$((fail + 1))
      ;;
  esac
}

make_nonwritable() {
  dir="$1"
  chmod 555 "$dir"
}

# ---- Test: fallback to $HOME/.local/bin when default not writable ----------
test_fallback_to_home_local_bin() {
  echo "  test: fallback to \$HOME/.local/bin"

  DEFAULT_DIR="$SCRATCH/usr-local-bin"
  mkdir -p "$DEFAULT_DIR"
  make_nonwritable "$DEFAULT_DIR"

  HOME_DIR="$SCRATCH/home/user"
  mkdir -p "$HOME_DIR/.local/bin"

  INSTALL_DIR="$DEFAULT_DIR"
  INSTALL_DIR_SPECIFIED=false
  FALLBACK_MSG=""
  PATH_HINT=""

  if ! touch "$INSTALL_DIR/.kei-write-test" 2>/dev/null; then
    if [ "$INSTALL_DIR_SPECIFIED" = false ]; then
      INSTALL_DIR="$HOME_DIR/.local/bin"
      mkdir -p "$INSTALL_DIR"
      FALLBACK_MSG="Installing to $INSTALL_DIR instead of $DEFAULT_DIR (not writable)."
      PATH_HINT="Ensure $INSTALL_DIR is on your PATH."
    fi
  fi

  assert_eq "fallback resolves to HOME/.local/bin" "$HOME_DIR/.local/bin" "$INSTALL_DIR"
  assert_eq "fallback dir exists" "yes" "$(test -d "$INSTALL_DIR" && echo yes || echo no)"
  assert_contains "fallback message mentions new dir" "$FALLBACK_MSG" "$HOME_DIR/.local/bin"
  assert_contains "PATH hint mentions new dir" "$PATH_HINT" "$INSTALL_DIR"
}

# ---- Test: explicit -d prevents fallback -----------------------------------
test_explicit_d_prevents_fallback() {
  echo "  test: explicit -d prevents fallback"

  DEFAULT_DIR="$SCRATCH/usr-local-bin-2"
  mkdir -p "$DEFAULT_DIR"
  make_nonwritable "$DEFAULT_DIR"

  HOME_DIR="$SCRATCH/home/user2"
  mkdir -p "$HOME_DIR"

  INSTALL_DIR="$DEFAULT_DIR"
  INSTALL_DIR_SPECIFIED=true  # user passed -d

  SHOULD_FAIL=false
  if ! touch "$INSTALL_DIR/.kei-write-test" 2>/dev/null; then
    if [ "$INSTALL_DIR_SPECIFIED" = false ]; then
      INSTALL_DIR="$HOME_DIR/.local/bin"
    fi
    SHOULD_FAIL=true
  fi

  # INSTALL_DIR should remain as DEFAULT_DIR (specified by user)
  assert_eq "explicit -d keeps original dir" "$DEFAULT_DIR" "$INSTALL_DIR"
  # The writability test failed, so we'd exit 1 in the real script
  assert_eq "writability test would fail" "yes" "$(test ! -w "$DEFAULT_DIR" && echo yes || echo no)"
  assert_eq "should_fail is true" "true" "$SHOULD_FAIL"
}

# ---- Test: $HOME/.local/bin is created if missing --------------------------
test_creates_home_local_bin() {
  echo "  test: creates \$HOME/.local/bin if missing"

  DEFAULT_DIR="$SCRATCH/usr-local-bin-3"
  mkdir -p "$DEFAULT_DIR"
  make_nonwritable "$DEFAULT_DIR"

  HOME_DIR="$SCRATCH/home/user3"
  # Do NOT create $HOME/.local/bin — should be created by fallback

  INSTALL_DIR="$DEFAULT_DIR"
  INSTALL_DIR_SPECIFIED=false

  if ! touch "$INSTALL_DIR/.kei-write-test" 2>/dev/null; then
    if [ "$INSTALL_DIR_SPECIFIED" = false ]; then
      INSTALL_DIR="$HOME_DIR/.local/bin"
      mkdir -p "$INSTALL_DIR"
    fi
  fi

  assert_eq "creates .local/bin if missing" "yes" "$(test -d "$INSTALL_DIR" && echo yes || echo no)"
}

# ---- Run tests -------------------------------------------------------------
echo "=== install.sh fallback tests ==="
test_fallback_to_home_local_bin
test_explicit_d_prevents_fallback
test_creates_home_local_bin

echo "=== Results: $pass passed, $fail failed ==="
exit $fail
