#!/usr/bin/env bash
# release.sh — Build and notarize an Atelier macOS release.
#
# Usage:
#   ./release.sh <semver>
#   ./release.sh --dry-run <semver>
#
# Prerequisites:
#   - Go, Node.js, Wails CLI v2, Xcode with notarytool
#   - A valid Developer ID Application certificate in your keychain
#   - Notarytool credentials. Either:
#       1) a keychain profile created with:
#            xcrun notarytool store-credentials \
#              --apple-id YOUR_APPLE_ID --team-id YOUR_TEAM_ID \
#              --password YOUR_APP_SPECIFIC_PASSWORD atelier-release
#          and set NOTARYTOOL_KEYCHAIN_PROFILE=atelier-release
#       2) env vars APPLE_ID, APPLE_TEAM_ID, APPLE_APP_SPECIFIC_PASSWORD
#
# The script updates wails.json/frontend/package.json with the supplied version,
# commits the bump, creates a Git tag, builds a universal macOS .app, signs it,
# packages a signed .dmg, and notarizes + staples the result.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"
APP_NAME="Atelier"
PRODUCT_BUNDLE_ID="com.wails.Atelier"

cd "$REPO_ROOT"

usage() {
    cat <<EOF
Usage: $0 [--dry-run] [--skip-tests] <semver>

  --dry-run      Update files and build/sign locally, but do not commit, tag, or notarize.
  --skip-tests   Skip running Go tests before building.
EOF
}

# ---------- Parse args ----------
DRY_RUN=false
SKIP_TESTS=false
VERSION=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --skip-tests)
            SKIP_TESTS=true
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        -*)
            echo "Unknown option: $1" >&2
            usage >&2
            exit 1
            ;;
        *)
            if [[ -n "$VERSION" ]]; then
                echo "Error: only one version argument allowed" >&2
                usage >&2
                exit 1
            fi
            VERSION="$1"
            shift
            ;;
    esac
done

if [[ -z "$VERSION" ]]; then
    echo "Error: version argument is required" >&2
    usage >&2
    exit 1
fi

# ---------- Validate semver ----------
semver_re='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'
if ! [[ "$VERSION" =~ $semver_re ]]; then
    echo "Error: '$VERSION' is not a valid semver version" >&2
    exit 1
fi

echo "==> Releasing Atelier v${VERSION}"

# ---------- Locate Wails CLI ----------
WAILS_BIN=""
if command -v wails >/dev/null 2>&1; then
    WAILS_BIN="wails"
elif [[ -x "$(go env GOPATH)/bin/wails" ]]; then
    WAILS_BIN="$(go env GOPATH)/bin/wails"
else
    echo "Error: wails CLI not found in PATH or ~/go/bin" >&2
    exit 1
fi
echo "    Wails CLI: $WAILS_BIN ($($WAILS_BIN version | head -1))"

# ---------- Validate tools ----------
for tool in git go npm python3 xcrun codesign hdiutil; do
    if ! command -v "$tool" >/dev/null 2>&1; then
        echo "Error: required tool '$tool' not found" >&2
        exit 1
    fi
done

# ---------- Resolve signing identity ----------
if [[ -z "${APPLE_DEVELOPER_ID_APPLICATION:-}" ]]; then
    APPLE_DEVELOPER_ID_APPLICATION=$(security find-identity -v -p codesigning 2>/dev/null \
        | grep 'Developer ID Application' \
        | head -1 \
        | sed -E 's/.*"([^"]+)".*/\1/' || true)
fi

if [[ -z "$APPLE_DEVELOPER_ID_APPLICATION" ]]; then
    echo "Error: no Developer ID Application certificate found" >&2
    echo "Set APPLE_DEVELOPER_ID_APPLICATION or install the certificate in your keychain." >&2
    exit 1
fi

TEAM_ID=""
if [[ "$APPLE_DEVELOPER_ID_APPLICATION" =~ \(([A-Z0-9]+)\)$ ]]; then
    TEAM_ID="${BASH_REMATCH[1]}"
fi
if [[ -z "$TEAM_ID" ]]; then
    echo "Error: could not extract team ID from signing identity: $APPLE_DEVELOPER_ID_APPLICATION" >&2
    exit 1
fi
echo "    Signing identity: $APPLE_DEVELOPER_ID_APPLICATION"
echo "    Team ID: $TEAM_ID"

# ---------- Resolve notarization credentials ----------
if [[ -n "${NOTARYTOOL_KEYCHAIN_PROFILE:-}" ]]; then
    echo "    Notarization: keychain profile '$NOTARYTOOL_KEYCHAIN_PROFILE'"
    NOTARY_ARGS=(--keychain-profile "$NOTARYTOOL_KEYCHAIN_PROFILE")
elif [[ -n "${APPLE_ID:-}" && -n "${APPLE_TEAM_ID:-}" && -n "${APPLE_APP_SPECIFIC_PASSWORD:-}" ]]; then
    echo "    Notarization: Apple ID credentials"
    NOTARY_ARGS=(--apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" --password "$APPLE_APP_SPECIFIC_PASSWORD")
else
    echo "Error: notarytool credentials not configured" >&2
    echo "Set one of:" >&2
    echo "  NOTARYTOOL_KEYCHAIN_PROFILE" >&2
    echo "  APPLE_ID + APPLE_TEAM_ID + APPLE_APP_SPECIFIC_PASSWORD" >&2
    exit 1
fi

# ---------- Ensure clean working tree (tracked files) ----------
if [[ "$DRY_RUN" != true ]]; then
    if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
        echo "Error: working tree has uncommitted changes" >&2
        echo "Commit or stash them before running a release." >&2
        exit 1
    fi
fi

# ---------- Update version files ----------
echo "==> Updating version files"

python3 - <<PY
import json

def json_update(path, setter):
    with open(path, 'r') as f:
        data = json.load(f)
    setter(data)
    with open(path, 'w') as f:
        json.dump(data, f, indent=2)
        f.write('\n')

def set_wails_version(data):
    data.setdefault('info', {})
    data['info']['productVersion'] = '$VERSION'

def set_package_version(data):
    data['version'] = '$VERSION'

json_update('wails.json', set_wails_version)
json_update('frontend/package.json', set_package_version)
PY

echo "    wails.json productVersion -> $VERSION"
echo "    frontend/package.json version -> $VERSION"

# ---------- Run tests ----------
if [[ "$SKIP_TESTS" != true ]]; then
    echo "==> Running tests"
    go test ./...
    echo "    Go tests passed"
    (cd frontend && npm run build)
    echo "    Frontend build passed"
else
    echo "==> Skipping tests"
fi

# ---------- Commit and tag ----------
if [[ "$DRY_RUN" != true ]]; then
    echo "==> Committing version bump"
    git add wails.json frontend/package.json
    git commit -m "chore(release): bump version to v${VERSION}"

    echo "==> Tagging release"
    git tag -a "v${VERSION}" -m "Atelier v${VERSION}"
fi

# ---------- Build universal macOS app ----------
echo "==> Building universal macOS app"

WAILS_BUILD_FLAGS=(
    -platform darwin/universal
    -ldflags "-X main.version=${VERSION} -s -w"
    -trimpath
)

rm -rf build/bin
"$WAILS_BIN" build "${WAILS_BUILD_FLAGS[@]}"

APP_PATH="build/bin/${APP_NAME}.app"
if [[ ! -d "$APP_PATH" ]]; then
    echo "Error: expected app bundle not found at $APP_PATH" >&2
    exit 1
fi

# ---------- Sign the app ----------
echo "==> Signing $APP_PATH"
codesign \
    --deep \
    --force \
    --timestamp \
    --options runtime \
    --entitlements build/darwin/entitlements.plist \
    --sign "$APPLE_DEVELOPER_ID_APPLICATION" \
    --verbose \
    "$APP_PATH"

codesign --verify --verbose "$APP_PATH"

# ---------- Package as signed DMG ----------
DMG_PATH="build/bin/${APP_NAME}-${VERSION}.dmg"
TMP_DMG="build/bin/${APP_NAME}-${VERSION}-tmp.dmg"
VOL_NAME="${APP_NAME} ${VERSION}"
MOUNT_POINT="/Volumes/${VOL_NAME}"

echo "==> Creating $DMG_PATH"
rm -f "$TMP_DMG" "$DMG_PATH"
hdiutil detach "$MOUNT_POINT" -quiet || true

hdiutil create \
    -srcfolder "$APP_PATH" \
    -volname "$VOL_NAME" \
    -fs HFS+ \
    -format UDRW \
    -size 200m \
    "$TMP_DMG"

DEVICE=$(hdiutil attach -readwrite -noverify "$TMP_DMG" | grep -E '^/dev/' | sed 1q | awk '{print $1}')

finalize_dmg() {
    hdiutil detach "$DEVICE" -force || true
    hdiutil convert "$TMP_DMG" -format UDZO -imagekey zlib-level=9 -o "$DMG_PATH"
    rm -f "$TMP_DMG"
}
trap finalize_dmg EXIT

ln -sf /Applications "/Volumes/${VOL_NAME}/Applications"

sleep 2
hdiutil detach "$DEVICE" || true
trap - EXIT
finalize_dmg

codesign \
    --force \
    --timestamp \
    --sign "$APPLE_DEVELOPER_ID_APPLICATION" \
    "$DMG_PATH"

# ---------- Notarize and staple ----------
if [[ "$DRY_RUN" != true ]]; then
    echo "==> Submitting DMG for notarization"
    xcrun notarytool submit "$DMG_PATH" --wait "${NOTARY_ARGS[@]}"

    echo "==> Stapling notarization ticket"
    xcrun stapler staple "$DMG_PATH"
    xcrun stapler validate "$DMG_PATH"
else
    echo "==> Dry run: skipping notarization"
fi

echo ""
echo "==> Release artifacts"
echo "    App:  $APP_PATH"
echo "    DMG:  $DMG_PATH"
if [[ "$DRY_RUN" != true ]]; then
    echo "    Tag:  v${VERSION}"
fi
