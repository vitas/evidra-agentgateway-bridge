#!/usr/bin/env bash
set -euo pipefail

dockerignore=${1:-.dockerignore}

require_rule() {
  local rule=$1
  grep -Fxq -- "$rule" "$dockerignore" || {
    echo "$dockerignore must exclude $rule from the Docker build context" >&2
    return 1
  }
}

for rule in \
  '.env' '.env.*' '**/.env' '**/.env.*' \
  '*.pem' '**/*.pem' '*.key' '**/*.key' '*.p12' '**/*.p12' '*.pfx' '**/*.pfx' \
  '*credential*' '**/*credential*' '*credentials*' '**/*credentials*' \
  '*token*' '**/*token*'; do
  require_rule "$rule"
done

# Secret-looking files must never be re-included with a negation rule. There are currently no
# environment examples required by the image build; add a narrowly named safe example here if
# that changes rather than permitting a whole secret-bearing class.
if grep -Eq '^!(.*\/)?(\.env|.*(credential|credentials|token)|.*\.(pem|key|p12|pfx))' "$dockerignore"; then
  echo "$dockerignore re-includes a secret-looking artifact" >&2
  exit 1
fi
