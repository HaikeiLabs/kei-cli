#!/bin/sh
# Validate the semver regex used in .github/workflows/release.yaml.
# POSIX ERE: \d is not supported, must use [0-9].
set -e

regex='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[a-zA-Z0-9._-]+)?$'

pass_count=0
fail_count=0

check() {
  tag="$1"
  expect="$2"
  if echo "$tag" | grep -qE "$regex"; then
    if [ "$expect" = "pass" ]; then
      pass_count=$((pass_count + 1))
    else
      echo "FAIL: $tag matched but should NOT have"
      fail_count=$((fail_count + 1))
    fi
  else
    if [ "$expect" = "fail" ]; then
      pass_count=$((pass_count + 1))
    else
      echo "FAIL: $tag did NOT match but should have"
      fail_count=$((fail_count + 1))
    fi
  fi
}

check "v0.1.9"       pass
check "v0.1.10"      pass
check "v1.20.300"    pass
check "v1.2.3-rc.1"  pass
check "v1.2.3-alpha" pass
check "v01.2.3"      fail  # leading zero
check "v1.02.3"      fail
check "v1.2.03"      fail
check "v"            fail
check "v1.2"         fail
check "1.2.3"        fail  # missing v prefix
check "abc"          fail

echo "Semver regex validation: $pass_count passed, $fail_count failed"
[ "$fail_count" -eq 0 ]
