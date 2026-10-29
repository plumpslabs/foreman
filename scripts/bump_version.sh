#!/usr/bin/env bash
set -euo pipefail

# Foreman Semantic Version Bumper Script
# Usage:
#   ./scripts/bump_version.sh patch   # v0.1.3 -> v0.1.4
#   ./scripts/bump_version.sh minor   # v0.1.3 -> v0.2.0
#   ./scripts/bump_version.sh major   # v0.1.3 -> v1.0.0
#   ./scripts/bump_version.sh v0.1.4  # explicit version

VERSION_FILE="internal/version/version.go"

if [ ! -f "$VERSION_FILE" ]; then
    echo "❌ Error: $VERSION_FILE not found. Run this script from the repository root."
    exit 1
fi

CURRENT_RAW=$(grep -E 'Version\s*=\s*"[^"]+"' "$VERSION_FILE" | sed -E 's/.*"([^"]+)".*/\1/')
CURRENT_VER="${CURRENT_RAW#v}"

BUMP_TYPE="${1:-}"
if [ -z "$BUMP_TYPE" ]; then
    echo "❌ Error: provide a bump type (patch|minor|major) or an explicit version (vX.Y.Z)."
    exit 1
fi

IFS='.' read -r MAJOR MINOR PATCH <<< "$CURRENT_VER"

case "$BUMP_TYPE" in
    patch) PATCH=$((PATCH + 1)) ;;
    minor) MINOR=$((MINOR + 1)); PATCH=0 ;;
    major) MAJOR=$((MAJOR + 1)); MINOR=0; PATCH=0 ;;
    v*)
        NEW_VER="${BUMP_TYPE#v}"
        if ! echo "$NEW_VER" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
            echo "❌ Error: explicit version must look like v1.2.3."
            exit 1
        fi
        sed -i.bak -E "s/Version(\s*)=\s*\"[^\"]+\"/Version\1= \"v$NEW_VER\"/" "$VERSION_FILE"
        rm -f "$VERSION_FILE.bak"
        echo "✅ Foreman version set to v$NEW_VER"
        exit 0
        ;;
    *)
        echo "❌ Unknown bump type: $BUMP_TYPE (use patch, minor, major, or vX.Y.Z)"
        exit 1
        ;;
esac

NEW_VER="$MAJOR.$MINOR.$PATCH"
sed -i.bak -E "s/Version(\s*)=\s*\"[^\"]+\"/Version\1= \"v$NEW_VER\"/" "$VERSION_FILE"
rm -f "$VERSION_FILE.bak"
echo "✅ Foreman version bumped: v$CURRENT_VER -> v$NEW_VER"
