#!/bin/bash
set -e

# Tampilan header
echo "================================================="
echo "        Mobile IDE One-Line Installer           "
echo "================================================="
echo "Mulai ngundhuh lan nyetel Mobile IDE..."

# 1. Cek dependensi Git
if ! command -v git &> /dev/null; then
    echo "Error: git ora ketemu. Mangga instal git dhisik!"
    exit 1
fi

# 2. Cek dependensi Go (Golang)
if ! command -v go &> /dev/null; then
    echo "Error: go (Golang) ora ketemu. Mangga instal golang dhisik!"
    exit 1
fi

# 3. Clone repository menyang folder 'mobile-ide'
INSTALL_DIR="mobile-ide"
if [ -d "$INSTALL_DIR" ]; then
    echo "Folder '$INSTALL_DIR' wis ana. Nganyari kode saka GitHub..."
    cd "$INSTALL_DIR"
    git pull
else
    echo "Cloning repository saka GitHub..."
    git clone https://github.com/sodikinnaa/go-agy-ide.git "$INSTALL_DIR"
    cd "$INSTALL_DIR"
fi

# 4. Kompilasi binary Go
echo "Kompilasi source code..."
go build -o mobile-agy main.go

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
echo "Cara nglakokake server ing background:"
echo "  PORT=8080 ./mobile-agy > server.log 2>&1 &"
echo ""
echo "Cathetan: Sandi keamanan akses bakal otomatis"
echo "digawe ing file 'password.txt' nalika server"
echo "mlaku kaping pisanan."
echo "================================================="
