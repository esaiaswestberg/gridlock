#!/bin/bash

# Gridlock Installation Script
# This script downloads the latest version of Gridlock and installs it.

set -e

REPO="esaiaswestberg/gridlock"
GITHUB_API="https://api.github.com/repos/$REPO/releases/latest"

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case $ARCH in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    armv7l) ARCH="armv7" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

if [ "$OS" == "darwin" ]; then
    OS="darwin"
elif [ "$OS" == "linux" ]; then
    OS="linux"
else
    echo "Unsupported OS: $OS"; exit 1
fi

echo "Detecting latest version..."
LATEST_RELEASE=$(curl -s $GITHUB_API | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

if [ -z "$LATEST_RELEASE" ]; then
    echo "Failed to fetch latest release version."
    exit 1
fi

echo "Latest version: $LATEST_RELEASE"

FILENAME="gridlock_${OS}_${ARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/$REPO/releases/download/$LATEST_RELEASE/$FILENAME"

echo "Downloading $DOWNLOAD_URL..."
TMP_DIR=$(mktemp -d)
curl -L -o "$TMP_DIR/$FILENAME" "$DOWNLOAD_URL"

echo "Extracting..."
tar -xzf "$TMP_DIR/$FILENAME" -C "$TMP_DIR"

echo "Installing to /usr/local/bin/gridlock..."
sudo mv "$TMP_DIR/gridlock" /usr/local/bin/gridlock
chmod +x /usr/local/bin/gridlock

# Cleanup
rm -rf "$TMP_DIR"

echo "Gridlock installed successfully!"

# Path checking
if [[ ":$PATH:" != *":/usr/local/bin:"* ]]; then
    echo ""
    echo "Warning: /usr/local/bin is not in your PATH."
    echo "You may need to add it to your shell configuration (e.g., .bashrc or .zshrc):"
    echo "    export PATH=\$PATH:/usr/local/bin"
fi

echo "Run 'gridlock --help' to get started."
