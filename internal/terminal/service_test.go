package terminal

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func TestMultiSessionService(t *testing.T) {
	svc := NewService()

	// 1. Check default session auto-created
	defaultSess := svc.GetSession("")
	if defaultSess == nil {
		t.Fatal("Expected default session to be auto-created for empty ID")
	}
	if defaultSess.ID != "default" {
		t.Errorf("Expected default session ID to be 'default', got %q", defaultSess.ID)
	}

	// 2. Create new custom session
	sess1, err := svc.CreateSession("term-1")
	if err != nil {
		t.Fatalf("CreateSession('term-1') failed: %v", err)
	}
	if sess1 == nil || sess1.ID != "term-1" {
		t.Fatalf("Expected session term-1, got %v", sess1)
	}

	// 3. Get existing session
	gotSess1 := svc.GetSession("term-1")
	if gotSess1 != sess1 {
		t.Errorf("GetSession('term-1') returned wrong instance")
	}

	// 4. List sessions
	sessions := svc.ListSessions()
	if len(sessions) != 2 {
		t.Errorf("Expected 2 sessions in ListSessions(), got %d", len(sessions))
	}

	// 5. Close session
	svc.CloseSession("term-1")
	if svc.GetSession("term-1") != nil {
		t.Errorf("Expected term-1 to be nil after CloseSession")
	}

	sessionsAfterClose := svc.ListSessions()
	if len(sessionsAfterClose) != 1 {
		t.Errorf("Expected 1 session after CloseSession, got %d", len(sessionsAfterClose))
	}
}

func TestBackwardCompatibilityDefaultSession(t *testing.T) {
	svc := NewService()

	sess, err := svc.CreateSession("")
	if err != nil {
		t.Fatalf("CreateSession('') error: %v", err)
	}
	if sess.ID != "default" {
		t.Errorf("Expected default session ID, got %s", sess.ID)
	}

	err = svc.WriteInput("echo hello\n")
	if err == nil {
		t.Error("Expected error writing input to non-running session, got nil")
	}
}

func TestTerminalServiceLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping PTY test on Windows")
	}

	svc := NewService()
	tempDir := t.TempDir()

	err := svc.StartSession(tempDir)
	if err != nil {
		t.Fatalf("Failed to start session: %v", err)
	}

	sess := svc.GetSession("default")
	if sess == nil {
		t.Fatalf("Expected default session to exist")
	}

	if !sess.IsRunning() {
		t.Fatalf("Expected session to be running")
	}

	sess.mutex.Lock()
	cmd := sess.cmd
	sess.mutex.Unlock()

	if cmd == nil || cmd.Process == nil {
		t.Fatalf("Expected cmd.Process to be initialized")
	}

	pid := cmd.Process.Pid
	if pid <= 0 {
		t.Fatalf("Invalid PID: %d", pid)
	}

	ch := make(chan []byte, 100)
	svc.RegisterClient(ch)
	defer svc.UnregisterClient(ch)

	err = svc.WriteInput("echo hello_terminal\n")
	if err != nil {
		t.Fatalf("Failed to write input: %v", err)
	}

	received := false
	timeout := time.After(2 * time.Second)
Loop:
	for {
		select {
		case data := <-ch:
			if len(data) > 0 {
				received = true
				break Loop
			}
		case <-timeout:
			break Loop
		}
	}

	if !received {
		t.Log("Warning: Did not receive echo output within 2 seconds")
	}

	svc.KillSession()

	done := make(chan struct{})
	go func() {
		for {
			if !sess.IsRunning() {
				close(done)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Service is still marked as running after KillSession")
	}

	reaped := false
	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		p, err := os.FindProcess(pid)
		if err != nil || p == nil {
			reaped = true
			break
		}
		if errSig := p.Signal(syscall.Signal(0)); errSig != nil {
			reaped = true
			break
		}
	}

	if !reaped {
		t.Errorf("Process %d was not reaped after KillSession", pid)
	}
}

func TestHistoryTruncation_UTF8Boundary(t *testing.T) {
	svc := NewService()

	sample := "Karakter UTF-8: 🔥 Selamat datang di Mobile IDE! こんにちは世界\n"

	for i := 0; i < 2000; i++ {
		line := fmt.Sprintf("[%04d] %s", i, sample)
		svc.Broadcast([]byte(line))
	}

	sess := svc.GetSession("default")
	sess.clientMux.Lock()
	hist := sess.history
	sess.clientMux.Unlock()

	if len(hist) > 65536 {
		t.Errorf("Expected history length <= 65536, got %d", len(hist))
	}

	if !utf8.Valid(hist) {
		t.Errorf("History contains invalid UTF-8 after truncation!")
	}

	if bytes.Contains(hist, []byte("\uFFFD")) {
		t.Errorf("History contains U+FFFD replacement character after truncation!")
	}
}

func TestHistoryTruncation_ANSISequence(t *testing.T) {
	svc := NewService()

	ansiSample := "\x1b[31m[ERROR]\x1b[0m \x1b[1;32mProses berhasil!\x1b[0m \x1b[38;2;255;100;50mCustom RGB Color\x1b[0m\n"

	for i := 0; i < 2000; i++ {
		line := fmt.Sprintf("[%04d] %s", i, ansiSample)
		svc.Broadcast([]byte(line))
	}

	sess := svc.GetSession("default")
	sess.clientMux.Lock()
	hist := sess.history
	sess.clientMux.Unlock()

	if len(hist) > 65536 {
		t.Errorf("Expected history length <= 65536, got %d", len(hist))
	}

	if !utf8.Valid(hist) {
		t.Errorf("History contains invalid UTF-8 with ANSI sequences!")
	}

	safe, _ := isSafeBoundary(hist, 0)
	if !safe {
		t.Errorf("History start is not a safe UTF-8/ANSI boundary!")
	}
}

func TestRegisterClient_CleanHistoryBroadcast(t *testing.T) {
	svc := NewService()

	fullLine := []byte("Header Line\nContent Line: ")
	incompleteRune := []byte{0xE4, 0xBD}

	svc.Broadcast(append(fullLine, incompleteRune...))

	ch := make(chan []byte, 1)
	svc.RegisterClient(ch)
	defer svc.UnregisterClient(ch)

	var receivedHist []byte
	select {
	case receivedHist = <-ch:
	default:
		t.Fatal("Expected new client to receive history payload")
	}

	if !utf8.Valid(receivedHist) {
		t.Fatalf("Registered client received invalid UTF-8 history: %q (%v)", string(receivedHist), receivedHist)
	}

	if bytes.Contains(receivedHist, []byte("\uFFFD")) {
		t.Errorf("Registered client received U+FFFD replacement character!")
	}
}

func TestUTF8AndANSIBoundaryChecking(t *testing.T) {
	buf := []byte("Line 1\n\x1b[31mRed Line\x1b[0m\nMulti-byte: 🔥 Selamat!\n")

	cutIdx := bytes.Index(buf, []byte("\x1b[31m")) + 2
	safeCut := findNextSafeBoundary(buf, cutIdx)

	if safeCut < cutIdx {
		t.Errorf("Expected safeCut >= cutIdx (%d), got %d", cutIdx, safeCut)
	}

	sliced := buf[safeCut:]
	if !utf8.Valid(sliced) {
		t.Errorf("Sliced buffer is not valid UTF-8!")
	}
}
