#!/usr/bin/env bash
set -euo pipefail

# Foreman Universal Installer
# Installs the latest binary release from GitHub to /usr/local/bin or ~/.local/bin
# and bundles the OpenHands SDK (Python) so `foreman start` is run-ready.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/plumpslabs/foreman/main/scripts/install.sh | sh
#   # skip SDK auto-install with:  FOREMAN_INSTALL_ENGINE=0 curl ... | sh

REPO="plumpslabs/foreman"
BINARY_NAME="foreman"

# 1. Detect OS
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
    darwin) OS="darwin" ;;
    linux) OS="linux" ;;
    freebsd) OS="freebsd" ;;
    mingw*|msys*|cygwin*) OS="windows" ;;
    *)
        echo "❌ Unsupported operating system: $OS"
        exit 1
        ;;
esac

# 2. Detect Architecture
ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *)
        echo "❌ Unsupported architecture: $ARCH"
        exit 1
        ;;
esac

# 3. Target Install Directory
if [ -w "/usr/local/bin" ]; then
    INSTALL_DIR="/usr/local/bin"
elif [ -n "${XDG_BIN_HOME:-}" ] && [ -d "$XDG_BIN_HOME" ]; then
    INSTALL_DIR="$XDG_BIN_HOME"
else
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
fi

echo "🚀 Installing Foreman for $OS/$ARCH into $INSTALL_DIR..."

# 4. Fetch latest release version
LATEST_TAG=$(curl -s "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' || true)

EXT="tar.gz"
if [ "$OS" = "windows" ]; then
    EXT="zip"
fi

if [ -z "$LATEST_TAG" ]; then
    echo "⚠️ Could not resolve the latest release tag from GitHub."
    echo "   Falling back to building from source with Go."
    if command -v go >/dev/null 2>&1; then
        GOINSTALL_DIR="$(go env GOPATH)/bin"
        mkdir -p "$GOINSTALL_DIR"
        go install "github.com/$REPO/cmd/foreman@latest"
        BIN="$GOINSTALL_DIR/$BINARY_NAME"
        echo "✅ Installed via Go to $BIN"
        SKIP_DOWNLOAD=1
    else
        echo "❌ No release tag found and Go is not installed. Install Go or build from source."
        exit 1
    fi
fi

ARCHIVE_NAME="${BINARY_NAME}_${LATEST_TAG#v}_${OS}_${ARCH}.${EXT}"
DOWNLOAD_URL="https://github.com/$REPO/releases/download/$LATEST_TAG/$ARCHIVE_NAME"

if [ "${SKIP_DOWNLOAD:-0}" != "1" ]; then
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

echo "📥 Downloading $DOWNLOAD_URL..."
if curl -sLf "$DOWNLOAD_URL" -o "$TMP_DIR/$ARCHIVE_NAME"; then
    if [ "$EXT" = "zip" ]; then
        unzip -q "$TMP_DIR/$ARCHIVE_NAME" -d "$TMP_DIR"
    else
        tar -xzf "$TMP_DIR/$ARCHIVE_NAME" -C "$TMP_DIR"
    fi
    # Verify checksum against the release's checksums.txt (GoReleaser-published)
    # so a tampered/mismatched binary is never installed.
    echo "🔐 Verifying checksum..."
    if curl -sLf "https://github.com/$REPO/releases/download/$LATEST_TAG/checksums.txt" -o "$TMP_DIR/checksums.txt"; then
        if command -v sha256sum >/dev/null 2>&1; then
            ACTUAL=$(sha256sum "$TMP_DIR/$ARCHIVE_NAME" | awk '{print $1}')
        elif command -v shasum >/dev/null 2>&1; then
            ACTUAL=$(shasum -a 256 "$TMP_DIR/$ARCHIVE_NAME" | awk '{print $1}')
        else
            ACTUAL=""
        fi
        if [ -n "$ACTUAL" ]; then
            EXPECTED=$(grep -E "(^|[^A-Za-z0-9])$ARCHIVE_NAME([^A-Za-z0-9]|$)" "$TMP_DIR/checksums.txt" | awk '{print $1}' | head -1)
            if [ -n "$EXPECTED" ] && [ "$EXPECTED" != "$ACTUAL" ]; then
                echo "❌ Checksum mismatch! expected $EXPECTED got $ACTUAL"
                exit 1
            fi
            echo "✅ Checksum verified ($ACTUAL)"
        else
            echo "⚠️ No sha256 tool available; skipping checksum verification"
        fi
    else
        echo "⚠️ checksums.txt not published; skipping verification"
    fi
    # GoReleaser wraps each archive in a per-build directory
    # (e.g. foreman_0.1.0_darwin_arm64/foreman), so locate the binary rather
    # than assuming it sits at the archive root. Handles .exe on Windows too.
    BIN=$(find "$TMP_DIR" -type f \( -name "$BINARY_NAME" -o -name "${BINARY_NAME}.exe" \) ! -name '*.txt' ! -name '*.md' ! -name 'LICENSE' | head -1)
    if [ -z "$BIN" ]; then
        echo "❌ Could not locate the $BINARY_NAME binary inside the archive."
        exit 1
    fi
    mv "$BIN" "$INSTALL_DIR/$BINARY_NAME"
    if [ "$OS" = "windows" ]; then
        mv "$INSTALL_DIR/$BINARY_NAME" "$INSTALL_DIR/${BINARY_NAME}.exe" 2>/dev/null || true
    fi
    chmod +x "$INSTALL_DIR/$BINARY_NAME"
    echo "✅ Foreman successfully installed to $INSTALL_DIR/$BINARY_NAME"
    BIN="$INSTALL_DIR/$BINARY_NAME"
    [ "$OS" = "windows" ] && BIN="${BIN}.exe"
else
    # Fallback to `go install` if pre-built binary is not yet available
    if command -v go >/dev/null 2>&1; then
        echo "⚠️ Binary download failed, compiling from source with Go..."
        go install "github.com/$REPO/cmd/foreman@$LATEST_TAG"
        echo "✅ Installed via Go to $(go env GOPATH)/bin/foreman"
    else
        echo "❌ Failed to download prebuilt binary. Please check release availability or install with Go."
        exit 1
    fi
fi
fi

# 5. Verify installation
if command -v foreman >/dev/null 2>&1; then
    foreman version || foreman --version || true
else
    echo "💡 Note: Please ensure $INSTALL_DIR is in your PATH."
    echo "   export PATH=\"\$PATH:$INSTALL_DIR\""
fi

# 5b. Convenience alias `fm` next to the binary.
if [ -w "$INSTALL_DIR" ] && [ ! -e "$INSTALL_DIR/fm" ] && [ -n "${BIN:-}" ]; then
    ln -s "$BIN" "$INSTALL_DIR/fm" 2>/dev/null \
        && echo "🔗 Alias 'fm' dibuat di $INSTALL_DIR/fm"
fi

# 6. OpenHands SDK — bundled so `foreman start` is run-ready.
if command -v python3 >/dev/null 2>&1; then
    PYVER="$(python3 -c 'import sys; print("%d.%d" % sys.version_info[:2])' 2>/dev/null || true)"
    if python3 -c "import openhands.sdk" >/dev/null 2>&1; then
        echo "✅ OpenHands SDK sudah tersedia ($(python3 --version 2>&1))"
    elif [ "${FOREMAN_INSTALL_ENGINE:-1}" = "1" ]; then
        echo "📦 OpenHands SDK belum terpasang — menginstal otomatis…"
        PY_OK="$(python3 -c 'import sys; print(1 if sys.version_info >= (3, 12) else 0)' 2>/dev/null || echo 0)"
        if [ "$PY_OK" != "1" ]; then
            echo "⚠️ SDK membutuhkan Python >=3.12; kamu punya $PYVER."
            echo "   Pasang Python 3.12+ lalu jalankan: python3 -m pip install openhands-sdk openhands-tools"
        elif python3 -m pip install --upgrade openhands-sdk openhands-tools; then
            echo "✅ OpenHands SDK terpasang ($(python3 --version 2>&1))"
        elif python3 -m pip install --upgrade --break-system-packages openhands-sdk openhands-tools; then
            echo "✅ OpenHands SDK terpasang (--break-system-packages) ($(python3 --version 2>&1))"
        else
            echo "⚠️ Gagal menginstal SDK otomatis. Jalankan manual:"
            echo "   python3 -m pip install openhands-sdk openhands-tools"
        fi
    else
        echo "⚠️ OpenHands SDK belum terpasang — jalankan nanti:"
        echo "   foreman doctor --install"
    fi
else
    echo "⚠️ python3 tidak ditemukan. Agent engine membutuhkan Python 3.12+ dengan OpenHands SDK."
fi
