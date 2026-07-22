#!/bin/bash
set -e

resolve_latest_version() {
    local tags testing_tag latest_tag
    tags=$(curl -fsSL "https://api.github.com/repos/sodikinnaa/go-agy-ide/releases?per_page=100" 2>/dev/null | grep '"tag_name":' | sed -E 's/.*"tag_name": "([^"]+)".*/\1/' || true)
    if [ -n "$tags" ]; then
        latest_tag=$(printf '%s\n' "$tags" | grep -E '^v[0-9]+(\.[0-9]+)*$' | sort -V | tail -n 1 || true)
        if [ -n "$latest_tag" ]; then
            echo "$latest_tag"
            return
        fi
        testing_tag=$(printf '%s\n' "$tags" | grep -E '^v[0-9]+\.[0-9]+\.testing\.[0-9]+$' | sort -V | tail -n 1 || true)
        if [ -n "$testing_tag" ]; then
            echo "$testing_tag"
            return
        fi
        printf '%s\n' "$tags" | head -n 1
        return
    fi
    echo "v1.5.9"
}

REQUESTED_VERSION="${1:-${VERSION:-}}"
if [ -n "$REQUESTED_VERSION" ] && [ "$REQUESTED_VERSION" != "latest" ]; then
    VERSION="$REQUESTED_VERSION"
else
    VERSION=$(resolve_latest_version)
fi

# Tampilan header
echo "================================================="
echo "        Mobile IDE One-Line Installer           "
echo "================================================="
echo "Versi target: $VERSION"
echo "Mulai ngundhuh pre-compiled binary saka GitHub..."

# 1. Deteksi OS lan Arsitektur CPU
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
BINARY_NAME="mobile-agy"

mobile_agy_pids() {
    # Aja nganggo `pkill -f mobile-agy` amarga iso mateni wrapper `agy-mobile update` dhewe.
    pgrep -x mobile-agy 2>/dev/null || true
    pgrep -x mobile-agy.exe 2>/dev/null || true
}

mobile_agy_running() {
    [ -n "$(mobile_agy_pids)" ]
}

stop_mobile_agy() {
    local pids
    pids=$(mobile_agy_pids)
    if [ -n "$pids" ]; then
        kill $pids 2>/dev/null || true
    fi
}

case "$OS" in
    linux)
        case "$ARCH" in
            x86_64|amd64)
                BINARY_URL="https://github.com/sodikinnaa/go-agy-ide/releases/download/${VERSION}/mobile-agy-linux-amd64"
                ;;
            aarch64|arm64)
                BINARY_URL="https://github.com/sodikinnaa/go-agy-ide/releases/download/${VERSION}/mobile-agy-linux-arm64"
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
                BINARY_URL="https://github.com/sodikinnaa/go-agy-ide/releases/download/${VERSION}/mobile-agy-darwin-amd64"
                ;;
            arm64)
                BINARY_URL="https://github.com/sodikinnaa/go-agy-ide/releases/download/${VERSION}/mobile-agy-darwin-arm64"
                ;;
            *)
                echo "Error: Arsitektur CPU $ARCH ora didhukung kanggo MacOS."
                exit 1
                ;;
        esac
        ;;
    mingw*|msys*|cygwin*|windows*)
        # Windows environment nggunakake Bash (Git Bash / MSYS2)
        BINARY_URL="https://github.com/sodikinnaa/go-agy-ide/releases/download/${VERSION}/mobile-agy-windows-amd64.exe"
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

generate_scripts() {
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
set -e
TARGET_VERSION="${1:-${VERSION:-latest}}"
INSTALLER_TMP="${TMPDIR:-/tmp}/mobile-agy-install.sh"
echo "Mulai nglakokake update Mobile IDE menyang versi: $TARGET_VERSION"
curl -H 'Cache-Control: no-cache' -fsSL 'https://raw.githubusercontent.com/sodikinnaa/go-agy-ide/main/install.sh' -o "$INSTALLER_TMP"
exec env VERSION="$TARGET_VERSION" bash "$INSTALLER_TMP"
EOT
    chmod +x update.sh

    # Nggawe command global 'agy-mobile'
    CURRENT_AGY_MOBILE_PATH=$(which agy-mobile 2>/dev/null || echo "")
    if [ -n "$CURRENT_AGY_MOBILE_PATH" ] && [ -w "$CURRENT_AGY_MOBILE_PATH" ]; then
        TARGET_WRAPPER="$CURRENT_AGY_MOBILE_PATH"
    else
        TARGET_WRAPPER="$HOME/.local/bin/agy-mobile"
        mkdir -p "$HOME/.local/bin"
    fi
    echo "Nggawe script wrapper global 'agy-mobile' ing $TARGET_WRAPPER..."
    ABS_INSTALL_DIR="$(pwd)"
    cat <<EOF > "$TARGET_WRAPPER"
#!/bin/bash
# Antigravity Mobile IDE Wrapper CLI

INSTALL_DIR="$ABS_INSTALL_DIR"

mobile_agy_pids() {
    pgrep -x mobile-agy 2>/dev/null || true
    pgrep -x mobile-agy.exe 2>/dev/null || true
}

mobile_agy_running() {
    [ -n "\$(mobile_agy_pids)" ]
}

stop_mobile_agy() {
    local pids
    pids=\$(mobile_agy_pids)
    if [ -n "\$pids" ]; then
        kill \$pids 2>/dev/null || true
    fi
}

case "\$1" in
    start)
        echo "Starting Mobile IDE..."
        if mobile_agy_running; then
            echo "Mobile IDE is already running."
        else
            cd "\$INSTALL_DIR"
            if command -v setsid &>/dev/null; then
                setsid ./start.sh > server.log 2>&1 &
            else
                nohup ./start.sh > server.log 2>&1 &
            fi
            sleep 2
            if mobile_agy_running; then
                echo "Mobile IDE started successfully."
            else
                echo "Failed to start Mobile IDE. Check \$INSTALL_DIR/server.log for errors."
            fi
        fi
        ;;
    stop)
        echo "Stopping Mobile IDE..."
        stop_mobile_agy
        echo "Mobile IDE stopped."
        ;;
    restart)
        echo "Restarting Mobile IDE..."
        stop_mobile_agy
        sleep 1
        cd "\$INSTALL_DIR"
        if command -v setsid &>/dev/null; then
            setsid ./start.sh > server.log 2>&1 &
        else
            nohup ./start.sh > server.log 2>&1 &
        fi
        sleep 2
        echo "Mobile IDE restarted."
        ;;
    status)
        PID=\$(mobile_agy_pids)
        if [ -n "\$PID" ]; then
            echo "========================================="
            echo "        Mobile IDE Status: RUNNING       "
            echo "========================================="
            echo "Version  : $VERSION"
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
    logs)
        echo "=== Mobile IDE Authentication Logs ==="
        if [ -f "\$INSTALL_DIR/server.log" ]; then
            grep -i "\[AUTH" "\$INSTALL_DIR/server.log" | tail -n 100
        else
            echo "No server.log found in \$INSTALL_DIR"
        fi
        ;;
    log)
        if [ -f "\$INSTALL_DIR/server.log" ]; then
            if [ "\$2" == "-f" ] || [ "\$2" == "follow" ]; then
                tail -f "\$INSTALL_DIR/server.log"
            else
                tail -n 100 "\$INSTALL_DIR/server.log"
            fi
        else
            echo "No server.log found in \$INSTALL_DIR"
        fi
        ;;
    update)
        TARGET_VERSION="\${2:-latest}"
        INSTALLER_TMP="\${TMPDIR:-/tmp}/mobile-agy-install.sh"
        echo "Updating Mobile IDE to \$TARGET_VERSION..."
        curl -H 'Cache-Control: no-cache' -fsSL 'https://raw.githubusercontent.com/sodikinnaa/go-agy-ide/main/install.sh' -o "\$INSTALLER_TMP"
        exec env VERSION="\$TARGET_VERSION" bash "\$INSTALLER_TMP"
        ;;
    install-version)
        if [ -z "\$2" ]; then
            echo "Usage: agy-mobile install-version <tag>"
            echo "Example: agy-mobile install-version v1.4.1"
            exit 1
        fi
        TARGET_VERSION="\$2"
        INSTALLER_TMP="\${TMPDIR:-/tmp}/mobile-agy-install.sh"
        echo "Installing Mobile IDE version \$TARGET_VERSION..."
        curl -H 'Cache-Control: no-cache' -fsSL 'https://raw.githubusercontent.com/sodikinnaa/go-agy-ide/main/install.sh' -o "\$INSTALLER_TMP"
        exec env VERSION="\$TARGET_VERSION" bash "\$INSTALLER_TMP"
        ;;
    releases)
        curl -fsSL "https://api.github.com/repos/sodikinnaa/go-agy-ide/releases?per_page=30" | grep '"tag_name":' | sed -E 's/.*"tag_name": "([^"]+)".*/\1/'
        ;;
    uninstall)
        echo "Stopping Mobile IDE..."
        stop_mobile_agy
        CURRENT_AGY_MOBILE_PATH=\$(which agy-mobile 2>/dev/null || echo "")
        if [ -n "\$CURRENT_AGY_MOBILE_PATH" ] && [ -w "\$CURRENT_AGY_MOBILE_PATH" ]; then
            rm -f "\$CURRENT_AGY_MOBILE_PATH"
            echo "Removed global command '\$CURRENT_AGY_MOBILE_PATH'."
        else
            rm -f "\$HOME/.local/bin/agy-mobile"
            echo "Removed global command 'agy-mobile'."
        fi

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
        echo "Usage: agy-mobile {start|stop|restart|status|log|logs|update [version]|install-version <version>|releases|uninstall}"
        exit 1
        ;;
esac
EOF
    chmod +x "$TARGET_WRAPPER"
}

# 2.5. Mulai ngundhuh pre-compiled binary saka GitHub Releases

# 3. Ngundhuh binary anyar dhisik (Download first to minimize downtime!)
echo "Ngundhuh binary kanggo OS: $OS ($ARCH)..."
echo "Alamat URL: $BINARY_URL"

# Ngundhuh menyang file sauntara (.tmp) dhisik
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

if [[ "$BINARY_NAME" != *.exe ]]; then
    chmod +x "$TEMP_BINARY"
fi

# 4. Ganteni binary lawas nganggo metode Hot-Swap (Nyegah 'text file busy' lan zero downtime)
echo "Nindakake hot-swap binary..."
if [[ "$BINARY_NAME" == *.exe ]]; then
    # Ing Windows, mateni proses dhisik amarga file sistem ngunci file sing mlaku
    taskkill //F //IM mobile-agy.exe 2>/dev/null || true
    mv -f "$TEMP_BINARY" "$BINARY_NAME" 2>/dev/null || true
else
    # Ing Linux/Unix, ganti jeneng file sing lagi mlaku dhisik (diidini dening OS)
    if [ -f "$BINARY_NAME" ]; then
        mv -f "$BINARY_NAME" "${BINARY_NAME}.old" 2>/dev/null || true
    fi
    # Pindhah binary anyar menyang panggonan utama
    mv -f "$TEMP_BINARY" "$BINARY_NAME"

    # Mandhegake proses lawas sing saiki mlaku minangka .old tanpa mateni wrapper `agy-mobile update`.
    stop_mobile_agy
    sleep 0.2

    # Hapus file .old (bakal dibusak sakwise proses lawas mati)
    rm -f "${BINARY_NAME}.old" 2>/dev/null || true
fi

# 5.5. Mriksa, Nginstal, sarta Nganyari Google Antigravity CLI (agy / gemini cli)
echo "Mriksa Google Antigravity CLI (agy)..."
if ! command -v agy &> /dev/null && [ ! -f "$HOME/.local/bin/agy" ]; then
    echo "Google Antigravity CLI (agy) ora ditemokake. Mulai ngundhuh lan nginstal..."
    if ! curl -fsSL https://antigravity.google/cli/install.sh | bash; then
        echo "Pènget: Gagal nginstal Antigravity CLI kanthi otomatis."
        echo "Njenengan bisa nyoba nginstal manual nganggo perintah:"
        echo "  curl -fsSL https://antigravity.google/cli/install.sh | bash"
    fi
else
    echo "Google Antigravity CLI (agy) wis terinstal. Nyoba nganyari menyang versi paling anyar..."
    AGY_BIN="agy"
    if [ -f "$HOME/.local/bin/agy" ]; then
        AGY_BIN="$HOME/.local/bin/agy"
    fi
    $AGY_BIN update || echo "Pènget: Gagal nganyari Antigravity CLI."
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
fi

# Dapatkan DBUS address saka session utawa socket default
DBUS_ADDR="$DBUS_SESSION_BUS_ADDRESS"
if [ -z "$DBUS_ADDR" ]; then
    MY_UID=$(id -u)
    if [ -S "/run/user/$MY_UID/bus" ]; then
        DBUS_ADDR="unix:path=/run/user/$MY_UID/bus"
    fi
fi

# Tulis/nganyari file konfigurasi .env tanpa mbusak setelan liyane (OPENAI_API_KEY, OPENAI_API_BASE, lsp.)
touch .env
set_env_var() {
    local key="$1"
    local value="$2"
    local escaped
    escaped=$(printf '%s' "$value" | sed 's/[&/\\]/\\&/g')
    if grep -qE "^${key}=" .env; then
        sed -i.bak "s/^${key}=.*/${key}=${escaped}/" .env && rm -f .env.bak
    else
        echo "${key}=${value}" >> .env
    fi
}

set_env_var "PORT" "$PORT"
set_env_var "PASSWORD" "$GEN_PASSWORD"
if [ -n "$DBUS_ADDR" ]; then
    set_env_var "DBUS_SESSION_BUS_ADDRESS" "$DBUS_ADDR"
fi

# Nggawe/nganyari kabeh script start.sh, update.sh, lan agy-mobile
generate_scripts

# 8. Nglakokake server ing background
echo "Nglakokake server Mobile IDE ing port: $PORT..."
if command -v setsid &>/dev/null; then
    setsid ./start.sh > server.log 2>&1 &
else
    nohup ./start.sh > server.log 2>&1 &
fi

# Ngenteni 2 detik kanggo mriksa apa server kasil munggah
sleep 2

# Cek apa process isih mlaku
SERVER_RUNNING=false
if [[ "$BINARY_NAME" == *.exe ]]; then
    if pgrep -x mobile-agy.exe > /dev/null 2>&1; then
        SERVER_RUNNING=true
    fi
else
    if pgrep -x mobile-agy > /dev/null 2>&1; then
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
