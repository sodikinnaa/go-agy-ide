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

# 6. Takon Port Keinginan User (Interactive Port Selection)
echo ""
echo "-------------------------------------------------"
read -p "Mlebokake Port kanggo server Mobile IDE (Default: 8080): " USER_PORT
PORT="8080"
if [ -n "$USER_PORT" ]; then
    if [[ "$USER_PORT" =~ ^[0-9]+$ ]]; then
        PORT="$USER_PORT"
    else
        echo "Format port salah. Nggunakake port default 8080."
    fi
fi

# 7. Nglakokake server ing background
echo "Nglakokake server Mobile IDE ing port: $PORT..."
if [[ "$BINARY_NAME" == *.exe ]]; then
    PORT=$PORT ./mobile-agy.exe > server.log 2>&1 &
else
    PORT=$PORT ./mobile-agy > server.log 2>&1 &
fi

# Ngenteni 2 detik kanggo mriksa apa server kasil munggah
sleep 2

# Cek apa process isih mlaku
SERVER_RUNNING=false
if [[ "$BINARY_NAME" == *.exe ]]; then
    if ps -ef | grep mobile-agy.exe | grep -v grep > /dev/null; then
        SERVER_RUNNING=true
    fi
else
    if ps -ef | grep mobile-agy | grep -v grep > /dev/null; then
        SERVER_RUNNING=true
    fi
fi

# 8. Tampilan Rampung
echo "================================================="
echo "                INSTALLASI SUKSES!               "
echo "================================================="
echo "Mobile IDE kasil disetel ing folder: $(pwd)"
echo "-------------------------------------------------"

if [ "$SERVER_RUNNING" = true ]; then
    echo "Server wis mlaku ing background!"
    echo "Bukak browser lan bukak alamat iki:"
    echo "  http://localhost:$PORT"
    echo ""
    echo "Kanggo mriksa log server, ketik:"
    echo "  cat server.log"
else
    echo "Server gagal mlaku otomatis (kemungkinan port $PORT wis dienggo)."
    echo "Njenengan bisa nglakokake server kanthi manual nganggo port liyane:"
    if [[ "$BINARY_NAME" == *.exe ]]; then
        echo "  PORT=9090 ./mobile-agy.exe"
    else
        echo "  PORT=9090 ./mobile-agy > server.log 2>&1 &"
    fi
fi

echo ""
echo "Cathetan: Sandi keamanan akses wis dimuat/digawe"
echo "ing file 'password.txt' ing folder iki."
echo "================================================="
