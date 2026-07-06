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

# 2. Nggawe folder instalasi (Cek supaya ora nggawe folder nested yen wis ana ing folder mobile-ide)
if [ "$(basename "$(pwd)")" != "mobile-ide" ]; then
    INSTALL_DIR="mobile-ide"
    mkdir -p "$INSTALL_DIR"
    cd "$INSTALL_DIR"
fi

# 3. Mandhegake proses lawas sarta ngilangi file lawas (Nyegah error lock/write permission)
echo "Mriksa lan ngresiki proses lawas..."
if [[ "$BINARY_NAME" == *.exe ]]; then
    taskkill //F //IM mobile-agy.exe 2>/dev/null || true
else
    # Mandhegake proses mobile-agy sarta start.sh sing isih mlaku
    pkill -f mobile-agy 2>/dev/null || true
    # Ngenteni sedhela supaya proses bener-bener mati
    sleep 1
fi

# Nyoba mbusak file lawas kanthi aman
if [ -f "$BINARY_NAME" ]; then
    rm -f "$BINARY_NAME" 2>/dev/null || {
        echo "Pènget: Gagal mbusak $BINARY_NAME lawas secara langsung. Nyoba ngganti jeneng..."
        mv -f "$BINARY_NAME" "${BINARY_NAME}.old" 2>/dev/null || true
    }
fi

# 4. Ngundhuh binary anyar
echo "Ngundhuh binary kanggo OS: $OS ($ARCH)..."
echo "Alamat URL: $BINARY_URL"

# Ngundhuh menyang file sauntara (.tmp) dhisik kanggo nyegah error write lock
TEMP_BINARY="${BINARY_NAME}.tmp"
rm -f "$TEMP_BINARY"

if ! curl -fL --no-progress-meter "$BINARY_URL" -o "$TEMP_BINARY"; then
    echo "================================================="
    echo "ERROR: Gagal ngundhuh binary saka GitHub!"
    echo "Priksa sambungan internet utawa limitasi jaringan."
    echo "Njenengan uga bisa ngundhuh manual saka URL ing ndhuwur."
    echo "================================================="
    exit 1
fi

# Ngalihake file sauntara dadi binary utama
mv -f "$TEMP_BINARY" "$BINARY_NAME" || {
    echo "ERROR: Gagal mindhah binary sauntara menyang $BINARY_NAME."
    echo "Kemungkinan file kasebut isih dienggo utawa ana masalah hak akses (permission)."
    exit 1
}

# 5. Setel permission executable (khusus non-Windows)
if [[ "$BINARY_NAME" != *.exe ]]; then
    chmod +x "$BINARY_NAME"
fi

# 5.5. Mriksa lan Nginstal Google Antigravity CLI (agy / gemini cli)
echo "Mriksa Google Antigravity CLI (agy)..."
if ! command -v agy &> /dev/null && [ ! -f "$HOME/.local/bin/agy" ]; then
    echo "Google Antigravity CLI (agy) ora ditemokake. Mulai ngundhuh lan nginstal..."
    if ! curl -fsSL https://antigravity.google/cli/install.sh | bash; then
        echo "Pènget: Gagal nginstal Antigravity CLI kanthi otomatis."
        echo "Njenengan bisa nyoba nginstal manual nganggo perintah:"
        echo "  curl -fsSL https://antigravity.google/cli/install.sh | bash"
    fi
else
    echo "Google Antigravity CLI (agy) wis terinstal."
fi

# Tambah ~/.local/bin menyang PATH yen durung ana ing session iki
if [[ ":$PATH:" != *":$HOME/.local/bin:"* ]]; then
    export PATH="$HOME/.local/bin:$PATH"
fi

# 6. Setel workspaces.json awal
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

# 7. Setel Konfigurasi (.env) - Ndhukung Fresh Install utawa Update
IS_UPDATE=false
if [ -f .env ]; then
    IS_UPDATE=true
    PORT=$(grep -E "^PORT=" .env | cut -d'=' -f2 || echo "8080")
    GEN_PASSWORD=$(grep -E "^PASSWORD=" .env | cut -d'=' -f2 || echo "AgyPass123")
    echo ""
    echo "Nemokake file konfigurasi .env (Mode Update)..."
    echo "Nggunakake port lawas  : $PORT"
    echo "Nggunakake sandi lawas : $GEN_PASSWORD"
else
    # Fresh Install: Takon Port Keinginan User
    echo ""
    echo "-------------------------------------------------"
    if [ -c /dev/tty ]; then
        read -p "Mlebokake Port kanggo server Mobile IDE (Default: 8080): " USER_PORT < /dev/tty
    else
        USER_PORT=""
    fi

    PORT="8080"
    if [ -n "$USER_PORT" ]; then
        if [[ "$USER_PORT" =~ ^[0-9]+$ ]]; then
            PORT="$USER_PORT"
        else
            echo "Format port salah. Nggunakake port default 8080."
        fi
    fi

    # Generate sandi keamanan acak (12 karakter)
    GEN_PASSWORD=$(tr -dc A-Za-z0-9 </dev/urandom | head -c 12 2>/dev/null || echo "AgyPass123")
    
    # Nggawe file konfigurasi .env anyar
    cat <<EOT > .env
PORT=$PORT
PASSWORD=$GEN_PASSWORD
EOT
fi

# Nggawe/nganyari script start.sh
cat <<'EOT' > start.sh
#!/bin/bash
# Tambah ~/.local/bin menyang PATH yen durung ana
if [[ ":$PATH:" != *":$HOME/.local/bin:"* ]]; then
    export PATH="$HOME/.local/bin:$PATH"
fi

if [ -f .env ]; then
    export $(grep -v '^#' .env | xargs)
fi

if [ -f ./mobile-agy.exe ]; then
    ./mobile-agy.exe
else
    ./mobile-agy
fi
EOT
chmod +x start.sh

# Nggawe/nganyari script update.sh supaya user gampang nglakokake update
cat <<'EOT' > update.sh
#!/bin/bash
echo "Mulai nglakokake update Mobile IDE..."
curl -fsSL "https://raw.githubusercontent.com/sodikinnaa/go-agy-ide/main/install.sh?v=$(date +%s)" | bash
EOT
chmod +x update.sh

# Nggawe command global 'agy-mobile' ing ~/.local/bin/agy-mobile
echo "Nggawe script wrapper global 'agy-mobile'..."
mkdir -p "$HOME/.local/bin"
ABS_INSTALL_DIR="$(pwd)"
cat <<EOF > "$HOME/.local/bin/agy-mobile"
#!/bin/bash
# Antigravity Mobile IDE Wrapper CLI

INSTALL_DIR="$ABS_INSTALL_DIR"

case "\$1" in
    start)
        echo "Starting Mobile IDE..."
        if pgrep -f "mobile-agy" > /dev/null; then
            echo "Mobile IDE is already running."
        else
            cd "\$INSTALL_DIR"
            ./start.sh > server.log 2>&1 &
            sleep 2
            if pgrep -f "mobile-agy" > /dev/null; then
                echo "Mobile IDE started successfully."
            else
                echo "Failed to start Mobile IDE. Check \$INSTALL_DIR/server.log for errors."
            fi
        fi
        ;;
    stop)
        echo "Stopping Mobile IDE..."
        pkill -f mobile-agy 2>/dev/null || true
        echo "Mobile IDE stopped."
        ;;
    restart)
        echo "Restarting Mobile IDE..."
        pkill -f mobile-agy 2>/dev/null || true
        sleep 1
        cd "\$INSTALL_DIR"
        ./start.sh > server.log 2>&1 &
        sleep 2
        echo "Mobile IDE restarted."
        ;;
    status)
        PID=\$(pgrep -f "mobile-agy" || true)
        if [ -n "\$PID" ]; then
            echo "========================================="
            echo "        Mobile IDE Status: RUNNING       "
            echo "========================================="
            echo "PID      : \$PID"
            if [ -f "\$INSTALL_DIR/.env" ]; then
                PORT=\$(grep -E "^PORT=" "\$INSTALL_DIR/.env" | cut -d'=' -f2)
                PASSWORD=\$(grep -E "^PASSWORD=" "\$INSTALL_DIR/.env" | cut -d'=' -f2)
                echo "Port     : \$PORT"
                echo "Password : \$PASSWORD"
                echo "Address  : http://localhost:\$PORT"
            fi
            echo "========================================="
        else
            echo "========================================="
            echo "        Mobile IDE Status: STOPPED       "
            echo "========================================="
        fi
        ;;
    update)
        echo "Updating Mobile IDE..."
        curl -fsSL "https://raw.githubusercontent.com/sodikinnaa/go-agy-ide/main/install.sh?v=\$(date +%s)" | bash
        ;;
    uninstall)
        echo "Stopping Mobile IDE..."
        pkill -f mobile-agy 2>/dev/null || true
        rm -f "\$HOME/.local/bin/agy-mobile"
        echo "Removed global command 'agy-mobile'."
        
        if [ -c /dev/tty ]; then
            read -p "Do you want to delete the installation directory (\$INSTALL_DIR)? (y/N): " choice < /dev/tty
        else
            choice="n"
        fi
        if [[ "\$choice" =~ ^[Yy]$ ]]; then
            rm -rf "\$INSTALL_DIR"
            echo "Installation directory (\$INSTALL_DIR) deleted."
        else
            echo "Installation directory left intact."
        fi
        echo "Mobile IDE successfully uninstalled."
        ;;
    *)
        echo "Usage: agy-mobile {start|stop|restart|status|update|uninstall}"
        exit 1
        ;;
esac
EOF
chmod +x "$HOME/.local/bin/agy-mobile"

# 8. Nglakokake server ing background
echo "Nglakokake server Mobile IDE ing port: $PORT..."
./start.sh > server.log 2>&1 &

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

# 9. Tampilan Rampung
echo "================================================="
echo "                INSTALLASI SUKSES!               "
echo "================================================="
echo "Mobile IDE kasil disetel ing folder: $(pwd)"
echo "-------------------------------------------------"
echo "Port Server    : $PORT"
echo "Sandi Akses    : $GEN_PASSWORD"
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
    echo "Njenengan bisa nglakokake server kanthi manual:"
    echo "  ./start.sh"
fi

echo ""
echo "Cathetan: Port sarta Sandi Akses wis disimpen ing file '.env'."
echo "Njenengan bisa ngowahi file '.env' kanggo custom."
echo "================================================="
