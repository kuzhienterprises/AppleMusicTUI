package auth

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"testing"
	"time"
)

// TestLoginCDPPlumbing verifies the machinery behind `am login` without
// needing credentials: launching a browser, reaching its debugger, connecting
// the WebSocket, and reading cookies back out.
//
// It runs headless so nothing appears on screen.
func TestLoginCDPPlumbing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser launch in short mode")
	}

	browser, err := FindBrowser()
	if err != nil {
		t.Skipf("no Chromium browser available: %v", err)
	}

	port, err := freePort()
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, browser,
		"--headless=new",
		"--user-data-dir="+t.TempDir(),
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--remote-allow-origins=*",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-gpu",
		"about:blank",
	)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the browser: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForDebugger(ctx, endpoint, 30*time.Second); err != nil {
		t.Fatalf("waitForDebugger: %v", err)
	}

	wsURL, err := pageTarget(ctx, endpoint)
	if err != nil {
		t.Fatalf("pageTarget: %v", err)
	}

	client, err := dialCDP(ctx, wsURL)
	if err != nil {
		t.Fatalf("dialCDP: %v", err)
	}
	defer client.close()

	// Round-trip a cookie through the same call `am login` uses to find the
	// session token.
	if err := client.call(ctx, "Network.setCookie", map[string]any{
		"name":   "probe",
		"value":  "ok",
		"domain": ".apple.com",
		"path":   "/",
	}, nil); err != nil {
		t.Fatalf("Network.setCookie: %v", err)
	}

	cookies, err := client.cookies(ctx)
	if err != nil {
		t.Fatalf("Storage.getCookies: %v", err)
	}

	value, found := findCookie(cookies, "probe")
	if !found {
		t.Fatalf("the probe cookie was not visible (read %d cookies)", len(cookies))
	}
	if value != "ok" {
		t.Fatalf("probe cookie value = %q, want %q", value, "ok")
	}
}
