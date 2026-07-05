# Antigravity Mobile IDE & Assistant

Aplikasi **Mobile IDE** sing enteng lan modern kanggo ngoding liwat HP Android nggunakake teknologi **Antigravity AI**.

## Fitur Utama
- **Touch-Friendly File Explorer**: Menu file lan folder sing gampang di-swipe lan di-klik ing layar HP.
- **Mobile Code Editor**: Nggunakake CodeMirror kanthi tema Dracula lan dhukungan syntax highlighting (Go, Python, JS, HTML, CSS).
- **Mobile Keyboard Shortcut Helper**: Tombol cepet ing ndhuwur keyboard HP kanggo ngetik karakter pemrograman (`{`, `}`, `[`, `]`, `;`, `=`, lan sakpiturute).
- **Interactive Chat Assistant**: Chatting real-time karo Antigravity AI, lengkap karo fitur **Copy** lan **Insert** kode menyang editor kanthi sekali klik.
- **Terminal Runner**: Nglakokake perintah terminal bash langsung saka HP.
- **REST API ready**: Kabeh fitur bisa diakses nganggo command `curl` liwat Termux/Terminal.

---

## Persyaratan Sistem
- **Go (Golang)**: Versi 1.16 utawa luwih anyar.
- **Antigravity CLI (`agy`)**: Wis terinstal lan terotentikasi ing server.
- **Bash**: Kanggo nglakokake perintah ing terminal console.

---

## Cara Instalasi & Kompilasi

### Cara Cepet (One-Line Installer):
Cukup jalankan perintah iki ing terminal server kanggo ngundhuh, ngompilasi, lan nyiapake kabeh project kanthi otomatis:
```bash
curl -fsSL https://raw.githubusercontent.com/sodikinnaa/go-agy-ide/main/install.sh | bash
```

### Cara Manual:
1. **Masuk menyang direktori project**:
   ```bash
   cd mobile-ide
   ```

2. **Kompilasi kode program**:
   ```bash
   go build -o mobile-agy main.go
   ```

3. **Jalankan server**:
   Njenengan bisa ngeset sandi keamanan liwat environment variable `PASSWORD`. Yen ora diset, server bakal nggawe sandi acak lan nampilake ing log server, sarta disimpen ing file `password.txt`.
   ```bash
   PASSWORD=sandi_njenengan PORT=8080 ./mobile-agy
   ```
   *Secara default, server bakal mlaku ing port `8080` lan ngrungokake kabeh antarmuka jaringan (`0.0.0.0:8080`).*

---

## Cara Akses saka HP Android

Kanggo ngakses kanthi aman lan lancar saka HP Android, disaranake nggunakake **SSH Port Forwarding**:

1. Bukak **Termux** utawa **Termius** ing HP Android.
2. Koneksi menyang server iki nggunakake perintah port forwarding:
   ```bash
   ssh -L 8080:localhost:8080 username@ip-server-mu
   ```
3. Sawise kasil konek, bukak **Google Chrome / browser liyane** ing HP, banjur bukak alamat:
   ```text
   http://localhost:8080
   ```
4. Lebokake sandi keamanan sing wis diset (misal: `sodikin123`) kanggo masuk.

---

## Dokumentasi API (Akses liwat `curl`)

Amarga saiki server langsung mriksa otentikasi Google Antigravity (`agy`) ing mesin, njenengan ora butuh cookie utawa sandi tambahan kanggo `curl`. Angger server wis login menyang Google, kabeh perintah `curl` ing ngisor iki iso langsung dijalankan saka HP Android (Termux):

* **Obrolan/Chat karo Antigravity (Streaming)**:
  ```bash
  curl -N -d "prompt=Buatkan endpoint HTTP GET baru" http://localhost:8080/api/chat
  ```

* **Nglakokake Perintah Terminal (Streaming)**:
  ```bash
  curl -N -d "command=go test ./..." http://localhost:8080/api/run
  ```

* **Maca Daftar File ing Workspace**:
  ```bash
  curl -s http://localhost:8080/api/files
  ```

* **Maca Isi File**:
  ```bash
  curl -s "http://localhost:8080/api/file?path=main.go"
  ```

* **Nyimpen / Nulis File**:
  ```bash
  curl -X POST -d "isi_kode_baru_di_sini" "http://localhost:8080/api/file?path=nama_file.go"
  ```

* **Mbusak File utawa Folder**:
  ```bash
  curl -X DELETE "http://localhost:8080/api/file?path=nama_file.go"
  ```

* **Maca Daftar Workspace (Recent & Active)**:
  ```bash
  curl -s http://localhost:8080/api/workspaces
  ```

* **Ngalih / Milih Workspace Aktif**:
  ```bash
  curl -d "path=/home/sodikinnaa/sodikin/project-lain" http://localhost:8080/api/workspaces/select
  ```

* **Nambah & Bukak Workspace Anyar**:
  ```bash
  curl -d "path=/home/sodikinnaa/sodikin/project-anyar" http://localhost:8080/api/workspaces/add
  ```



