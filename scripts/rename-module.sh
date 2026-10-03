#!/usr/bin/env bash
# Renames the Go module path everywhere it appears (go.mod is the single source of truth).
#   scripts/rename-module.sh github.com/acme/honk-go
# Go has no way to import a package without spelling the module path, so cmd/honk-me, the
# examples and the docs repeat it; this rewrites them all from go.mod's current value.
set -euo pipefail
cd "$(dirname "$0")/.."
new=${1:?usage: scripts/rename-module.sh NEW/MODULE/PATH}
old=$(go list -m)
[ "$old" = "$new" ] && { echo "already $new"; exit 0; }
go mod edit -module "$new"
files=$(grep -rl --exclude-dir=.git -F "$old" . ../README.md ../.github-workflows ../../examples/README.md 2>/dev/null || true)
for f in $files; do
  perl -pi -e "s#\Q$old\E#$new#g" "$f"
done
gofmt -l . | xargs -r gofmt -w
go build ./... && go vet ./...
echo "module renamed: $old -> $new"
echo "updated: $(echo "$files" | tr '\n' ' ')"
