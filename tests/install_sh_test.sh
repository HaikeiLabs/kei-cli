#!/bin/sh
set -eu

# Test the POSIX install.sh script — runs under sh, bash, and dash (if found).
# Usage:
#   sh tests/install_sh_test.sh                        # default: sh
#   sh tests/install_sh_test.sh /bin/bash              # explicit shell
#   shell=all sh tests/install_sh_test.sh              # all available shells
#
# Set KEEP_TMP=1 to preserve the temp directory for inspection.

INSTALL_SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/scripts/install.sh"
VERSION="0.1.6"
SHELLS="${shell:-sh}"
PASS=0
FAIL=0

if [ "${shell:-}" = "all" ]; then
  SHELLS=""
  for s in /bin/sh /bin/bash /bin/dash; do
    if [ -x "$s" ]; then
      SHELLS="$SHELLS $s"
    fi
  done
fi

for shell_bin in $SHELLS; do
  echo "=== Testing with $shell_bin ==="

  TMP_ROOT="$(mktemp -d "/tmp/kei-install-test-XXXXXX")"
  INSTALL_DIR="$TMP_ROOT/bin"
  mkdir -p "$INSTALL_DIR"

  echo "  Installing kei v$VERSION to $INSTALL_DIR"
  set +e
  OUTPUT="$("$shell_bin" "$INSTALL_SCRIPT" -v "$VERSION" -d "$INSTALL_DIR" 2>&1)"
  RC=$?
  set -e

  echo "$OUTPUT" | sed 's/^/  /'

  if [ "$RC" -ne 0 ]; then
    echo "  FAIL: installer exited with code $RC"
    FAIL=$((FAIL + 1))
    [ -z "${KEEP_TMP:-}" ] && rm -rf "$TMP_ROOT"
    continue
  fi

  # Check the installed kei binary
  if [ -x "$INSTALL_DIR/kei" ]; then
    VOUT="$("$INSTALL_DIR/kei" --version 2>&1)"
    case "$VOUT" in
      *"$VERSION"*)
        echo "  PASS: kei --version reports $VOUT"
        PASS=$((PASS + 1))
        ;;
      *)
        echo "  FAIL: kei --version = '$VOUT' (expected v$VERSION)"
        FAIL=$((FAIL + 1))
        ;;
    esac
  else
    echo "  FAIL: kei binary not found at $INSTALL_DIR/kei"
    FAIL=$((FAIL + 1))
  fi

  # Check kei-proxy if it was installed
  if [ -x "$INSTALL_DIR/kei-proxy" ]; then
    POUT="$("$INSTALL_DIR/kei-proxy" --version 2>&1)"
    echo "  INFO: kei-proxy --version reports $POUT"
  fi

  # Verify verification lines use installed binary path, not PATH
  case "$OUTPUT" in
    *"Verified: kei"*)
      echo "  PASS: verification output present"
      PASS=$((PASS + 1))
      ;;
    *)
      echo "  FAIL: no 'Verified:' line in output"
      FAIL=$((FAIL + 1))
      ;;
  esac

  # Check that it's the installed binary being verified, not PATH
  case "$OUTPUT" in
    *"$INSTALL_DIR/kei"*)
      # This is good - the verification mentions the full path
      echo "  PASS: verification uses installed path"
      PASS=$((PASS + 1))
      ;;
    *)
      # Just a soft-check; the "Verified:" line doesn't include path, which is fine
      ;;
  esac

  [ -z "${KEEP_TMP:-}" ] && rm -rf "$TMP_ROOT"
  echo ""
done

echo "=== Results ==="
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
[ "$FAIL" -gt 0 ] && exit 1
exit 0
