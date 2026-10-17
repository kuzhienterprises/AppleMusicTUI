package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kuzhienterprises/AppleMusicTUI/internal/apple"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/config"
	"github.com/kuzhienterprises/AppleMusicTUI/internal/ui"
)

// LoginResult is what a successful sign-in produced.
type LoginResult struct {
	Storefront string
}

// The web player stores the music user token in a cookie with this name. It
// is the same value the API expects in the Music-User-Token header.
const userTokenCookie = "media-user-token"

const loginTimeout = 10 * time.Minute

// Login launches the user's own browser against the Apple Music sign-in page,
// waits for the session cookie to appear, then stores it.
//
// A dedicated browser profile is used so the session persists between runs
// without ever touching the user's everyday Chrome profile.
func Login(ctx context.Context) (*LoginResult, error) {
	browserPath, err := FindBrowser()
	if err != nil {
		return nil, err
	}

	cfg := config.Load()
	storefront := cfg.Storefront
	if storefront == "" {
		storefront = apple.DetectStorefront(ctx)
	}

	// Fetch a developer token first: it is needed to verify the user token
	// the moment we capture it.
	devToken, err := ensureDevToken(ctx, cfg, storefront)
	if err != nil {
		return nil, err
	}

	port, err := freePort()
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(config.ProfileDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the browser profile directory: %w", err)
	}

	signInURL := fmt.Sprintf("https://music.apple.com/%s/sign-in", storefront)
	args := []string{
		"--user-data-dir=" + config.ProfileDir,
		fmt.Sprintf("--remote-debugging-port=%d", port),
		// Chrome refuses DevTools WebSocket connections unless we opt in.
		"--remote-allow-origins=*",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate,OptimizationHints,MediaRouter",
		"--new-window",
		signInURL,
	}

	ui.Step("Opening %s", filepath.Base(browserPath))
	cmd := exec.Command(browserPath, args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting the browser: %w", err)
	}
	defer func() {
		// Politely ask the browser to close, then make sure it is gone.
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	if err := waitForDebugger(ctx, endpoint, 30*time.Second); err != nil {
		return nil, err
	}

	wsURL, err := pageTarget(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	cdp, err := dialCDP(ctx, wsURL)
	if err != nil {
		return nil, err
	}
	defer cdp.close()

	ui.Info("")
	ui.Info("  Sign in with your Apple ID in the window that just opened.")
	ui.Info("  The session is detected automatically — you can leave the tab alone")
	ui.Info("  once you land back in the Apple Music player.")
	ui.Info("")

	userToken, err := waitForUserToken(ctx, cdp, loginTimeout)
	if err != nil {
		return nil, err
	}

	verifiedStorefront, err := VerifyUserToken(ctx, devToken, userToken)
	if err != nil {
		return nil, err
	}

	if _, err := config.Update(func(c *config.Config) {
		c.MusicUserToken = userToken
		c.Storefront = verifiedStorefront
		c.DevToken = devToken
	}); err != nil {
		return nil, err
	}

	// Closing the browser is best-effort; the deferred kill handles the rest.
	_ = cdp.call(context.Background(), "Browser.close", nil, nil)

	return &LoginResult{Storefront: verifiedStorefront}, nil
}

func waitForUserToken(ctx context.Context, cdp *cdpClient, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		cookies, err := cdp.cookies(ctx)
		if err != nil {
			lastErr = err
		} else if token, ok := findCookie(cookies, userTokenCookie); ok {
			return token, nil
		}
		time.Sleep(1500 * time.Millisecond)
	}

	if lastErr != nil {
		return "", fmt.Errorf("gave up waiting for sign-in: %w", lastErr)
	}
	return "", fmt.Errorf("timed out after %s without detecting a sign-in", timeout)
}

func waitForDebugger(ctx context.Context, endpoint string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 3 * time.Second}

	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := client.Get(endpoint + "/json/version")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("the browser did not expose a debugger on %s", endpoint)
}

// pageTarget returns the debugger WebSocket for the Apple Music tab.
func pageTarget(ctx context.Context, endpoint string) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		resp, err := client.Get(endpoint + "/json/list")
		if err != nil {
			time.Sleep(400 * time.Millisecond)
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			time.Sleep(400 * time.Millisecond)
			continue
		}

		var targets []struct {
			Type                 string `json:"type"`
			URL                  string `json:"url"`
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		if err := json.Unmarshal(body, &targets); err != nil {
			time.Sleep(400 * time.Millisecond)
			continue
		}

		// Prefer the Apple Music tab, but fall back to any page target.
		for _, target := range targets {
			if target.Type == "page" && target.WebSocketDebuggerURL != "" && strings.Contains(target.URL, "music.apple.com") {
				return target.WebSocketDebuggerURL, nil
			}
		}
		for _, target := range targets {
			if target.Type == "page" && target.WebSocketDebuggerURL != "" {
				return target.WebSocketDebuggerURL, nil
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	return "", fmt.Errorf("no debuggable page was found in the browser")
}

func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("finding a free port: %w", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// FindBrowser locates Chrome, falling back to Edge: both are Chromium and
// both speak the DevTools protocol.
func FindBrowser() (string, error) {
	var candidates []string

	if runtime.GOOS == "windows" {
		var roots []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
			if value := os.Getenv(env); value != "" {
				roots = append(roots, value)
			}
		}
		for _, root := range roots {
			candidates = append(candidates,
				filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
			)
		}
	} else if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		)
	}

	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}

	for _, name := range []string{"google-chrome", "chrome", "chromium", "microsoft-edge"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("could not find Chrome or Edge; install one, or sign in with `am login --token <value>`")
}
