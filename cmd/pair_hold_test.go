package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestPairHoldIdlesUntilSignal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENMESSAGES_DATA_DIR", dir)
	var out bytes.Buffer
	stop := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() { done <- pairHoldUntil(&out, stop) }()
	select {
	case <-done:
		t.Fatal("pairHold returned before a signal")
	case <-time.After(100 * time.Millisecond):
	}
	stop <- os.Interrupt
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pairHold did not exit on signal")
	}
	if got := strings.Count(out.String(), "\n"); got != 1 || !strings.Contains(out.String(), "idle for pairing") {
		t.Fatalf("output = %q, want one idle line", out.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("pair hold touched data dir: %v", entries)
	}
}

func TestRunServePairHoldBlocksWithoutTouchingDataDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENMESSAGES_DATA_DIR", dir)
	t.Setenv("PAIR_HOLD", "1")
	done := make(chan error, 1)
	go func() { done <- RunServe(zerolog.Nop()) }()
	select {
	case err := <-done:
		t.Fatalf("RunServe returned under PAIR_HOLD=1: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("PAIR_HOLD touched data dir: %v", entries)
	}
}
