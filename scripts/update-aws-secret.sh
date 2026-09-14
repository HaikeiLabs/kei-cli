#!/bin/bash
set -euo pipefail

# update-aws-secret.sh — update a single JSON property of an AWS Secrets
# Manager secret with a one-time runtime token read from stdin.
#
# The token is read only from stdin. It is never placed in argv, stdout,
# stderr, a temp filename, or shell history: it is written to a private temp
# file (0600, generic name) and handed to jq via --rawfile and to the AWS CLI
# via file://. All other JSON fields of the secret are preserved.
#
# Usage:
#   kei bot credential --installation ID --rotate \
#     | ./scripts/update-aws-secret.sh SECRET_ID [KEY]
#
#   SECRET_ID  AWS Secrets Manager secret id or name (e.g. kei-discord)
#   KEY        JSON property to update (default: kei_runtime_token)
#
# NOTE: run the pre-rotation preflight documented in README.md BEFORE invoking
# this. The rotation happens in the CLI before this script runs, so a failed
# destination check here cannot prevent the rotation — it only fails the write
# fast. If this script fails, the one-time token is consumed and you must
# rotate again.

usage() {
    echo "Usage: $(basename "$0") SECRET_ID [KEY]" >&2
    echo "  Reads the one-time token from stdin and updates KEY (default" >&2
    echo "  kei_runtime_token) in the JSON SecretString of the named AWS" >&2
    echo "  Secrets Manager secret, preserving all other fields." >&2
}

if [ "$#" -lt 1 ] || [ -z "${1:-}" ] || [ "$#" -gt 2 ]; then
    echo "Error: provide SECRET_ID and optionally KEY." >&2
    usage
    exit 1
fi

SECRET_ID="$1"
KEY="${2:-kei_runtime_token}"

for dep in jq aws; do
    if ! command -v "$dep" >/dev/null 2>&1; then
        echo "Error: $dep is required." >&2
        exit 1
    fi
done

TOKEN_FILE="$(mktemp)"
UPDATED_FILE="$(mktemp)"
trap 'rm -f "$TOKEN_FILE" "$UPDATED_FILE"' EXIT

# Read the one-time token from stdin only (a single line). The value lives in a
# shell variable and a private file, never in argv or the terminal.
TOKEN=""
IFS= read -r TOKEN || true
TOKEN="${TOKEN%$'\r'}"
if [ -z "$TOKEN" ]; then
    echo "Error: no token on stdin. Pipe it from: kei bot credential --installation ID --rotate" >&2
    exit 1
fi
printf '%s' "$TOKEN" > "$TOKEN_FILE"

# Preflight: the destination secret must exist and hold a JSON object before we
# accept the write. This runs after the CLI has already rotated, so it is a
# fast-fail guard on the write, not a way to prevent the rotation.
EXISTING=""
if ! EXISTING="$(aws secretsmanager get-secret-value --secret-id "$SECRET_ID" --query SecretString --output text 2>&1)"; then
    echo "Error: could not read secret '$SECRET_ID'." >&2
    printf '%s\n' "$EXISTING" >&2
    exit 1
fi
if [ -z "$EXISTING" ]; then
    echo "Error: secret '$SECRET_ID' has no SecretString." >&2
    exit 1
fi
if ! printf '%s' "$EXISTING" | jq -e 'type == "object"' >/dev/null 2>&1; then
    echo "Error: secret '$SECRET_ID' is not a JSON object." >&2
    exit 1
fi

# Update exactly one property, preserving all others. jq reads the token from
# the private file (--rawfile), so the token never appears in argv.
if ! printf '%s' "$EXISTING" | jq --rawfile tok "$TOKEN_FILE" --arg key "$KEY" '.[$key] = $tok' > "$UPDATED_FILE"; then
    echo "Error: failed to update JSON property '$KEY'." >&2
    exit 1
fi

# Put the updated value using file:// so the token is never in argv.
if ! aws secretsmanager put-secret-value --secret-id "$SECRET_ID" --secret-string "file://$UPDATED_FILE"; then
    echo "Error: failed to update secret '$SECRET_ID'." >&2
    exit 1
fi

echo "Updated property '$KEY' on secret '$SECRET_ID'."
