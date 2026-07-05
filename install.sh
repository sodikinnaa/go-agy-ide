#!/bin/bash
set -e

# Tampilan header
echo "================================================="
echo "        Mobile IDE One-Line Installer           "
echo "================================================="
echo "Mulai ngundhuh pre-compiled binary saka GitHub..."

# 1. Deteksi OS lan Arsitektur CPU
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
BINARY_NAME="mobile-agy"

case "$OS" in
    linux)
        case "$ARCH" in
            x86_64|amd64)
                BINARY_URL="https://github.com/sodikinnaa/go-agy-ide/releases/download/latest/mobile-agy-linux-amd64"
                ;;
            aarch64|arm64)
                BINARY_URL="https://github.com/sodikinnaa/go-agy-ide/releases/download/latest/mobile-agy-linux-arm64"
                ;;
            *)
                echo "Error: Arsitektur CPU $ARCH ora didhukung kanggo Linux."
                exit 1
                ;;
        esac
        ;;
    darwin)
        case "$ARCH" in
            x86_64|amd64)
                BINARY_URL="https://github.com/sodikinnaa/go-agy-ide/releases/download/latest/mobile-agy-darwin-amd64"
                ;;
            arm64)
                BINARY_URL="https://github.com/sodikinnaa/go-agy-ide/releases/download/latest/mobile-agy-darwin-arm64"
                ;;
            *)
                echo "Error: Arsitektur CPU $ARCH ora didhukung kanggo MacOS."
                exit 1
                ;;
        esac
        ;;
    mingw*|msys*|cygwin*|windows*)
        # Windows environment nggunakake Bash (Git Bash / MSYS2)
        BINARY_URL="https://github.com/sodikinnaa/go-agy-ide/releases/download/latest/mobile-agy-windows-amd64.exe"
        BINARY_NAME="mobile-agy.exe"
        ;;
    *)
        echo "Error: Sistem Operasi $OS ora didhukung."
        exit 1
        ;;
esac

# 2. Nggawe folder instalasi
INSTALL_DIR="mobile-ide"
mkdir -p "$INSTALL_DIR"
cd "$INSTALL_DIR"

# 3. Ngundhuh binary
echo "Ngundhuh binary kanggo OS: $OS ($ARCH)..."
curl -fsSL "$BINARY_URL" -o "$BINARY_NAME"

# 4. Setel permission executable (khusus non-Windows)
if [[ "$BINARY_NAME" != *.exe ]]; then
    chmod +x "$BINARY_NAME"
fi

# 5. Setel workspaces.json awal
if [ ! -f "workspaces.json" ]; then
    cat <<EOT > workspaces.json
{
  "active": "$(pwd)",
  "list": [
    "$(pwd)"
  ]
}
EOT
fi

# 6. Rampung
echo "================================================="
echo "                INSTALLASI SUKSES!               "
echo "================================================="
echo "Mobile IDE kasil disetel ing folder: $(pwd)"
echo "-------------------------------------------------"
echo "Cara nglakokake server:"
if [[ "$BINARY_NAME" == *.exe ]]; then
  echo "  PORT=8080 ./mobile-agy.exe"
else
  echo "  PORT=8080 ./mobile-agy > server.log 2>&1 &"
fi
echo ""
echo "Cathetan: Sandi keamanan akses bakal otomatis"
echo "digawe ing file 'password.txt' nalika server"
echo "mlaku kaping pisanan."
echo "================================================="
