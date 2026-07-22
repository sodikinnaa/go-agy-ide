# AGENTS.md

Welcome to the **Antigravity Mobile IDE** repository. This file provides critical context for agents to effectively assist with development.

## 📱 Project Overview
A lightweight, mobile-first IDE built with Go and Antigravity AI. It provides an optimized coding experience for Android (via Termux) and other mobile-capable terminal environments.

## 🚀 Key Commands
- **Install/Update**: Use the provided installer script for a pre-compiled binary:
  ```bash
  curl -fsSL https://raw.githubusercontent.com/sodikinnaa/go-agy-ide/main/install.sh | bash
  ```
- **Build**: This project is typically built as a static binary to ensure compatibility across Linux environments:
  ```bash
  CGO_ENABLED=0 go build -o mobile-agy .
  ```

## 🛠️ Development Conventions
- **Static Compilation**: The project relies on `CGO_ENABLED=0` to avoid GLIBC dependency issues on older or restricted Linux environments (like Termux).
- **Core Technology**: 
  - Backend: Go
  - Frontend (Editor): CodeMirror (Dracula theme)
  - Interactivity: Antigravity AI integration via embedded CLI commands.

## ⚠️ Known Quirks & Gotchas
- **Compatibility**: The binaries are built to be static, making them portable. Do not introduce dynamic CGO dependencies unless explicitly required for platform-specific hardware acceleration.
- **Mobile-First**: Features like `Touch-Friendly File Explorer` and `Keyboard Shortcut Helper` are integral to the UX. Any frontend changes MUST be verified in a mobile browser/Termux environment.
- **Entrypoints**: `main.go` is the primary entrypoint. Logic for IDE functionality is split between `main.go` and internal packages.

## 📋 Task Execution
- **Kelola Produk Kontrol Panel**: Untuk pekerjaan/fitur terkait "Kelola Produk Kontrol Panel", pastikan semua kebutuhan disimpan dalam berkas format `.md` di dalam folder `asset-marketing`.
- Always prefer `CGO_ENABLED=0` when building.
- Verify frontend changes using the `visual-engineering` category and relevant UI/UX skills.
- Test terminal runner behavior in a real bash environment (Termux preferred).
