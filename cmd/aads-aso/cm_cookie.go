package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newASOCMCookieCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cm-cookie",
		Short: "Export app-ads.apple.com Cookie header via interactive Playwright login (local-only)",
		Long: strings.TrimSpace(`
Exports a Cookie header value for app-ads.apple.com by opening a real browser (Playwright) and waiting for you to log in.

This is intended to refresh cookies used by:
  - aads aso popscore
  - aads aso recommend

Notes:
  - This is best-effort and may break if Apple changes login/session behavior.
  - You will still need to complete login/2FA in the browser; this command does not bypass it.
`),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			url, _ := cmd.Flags().GetString("url")
			url = strings.TrimSpace(url)
			if url == "" {
				url = "https://app-ads.apple.com/"
			}

			headed, _ := cmd.Flags().GetBool("headed")
			profileDir, _ := cmd.Flags().GetString("profile-dir")
			profileDir = strings.TrimSpace(profileDir)
			if profileDir == "" {
				profileDir = defaultCMCookieProfileDir()
			}

			outPath, _ := cmd.Flags().GetString("out")
			outPath = strings.TrimSpace(outPath)

			closeBrowser, _ := cmd.Flags().GetBool("close")
			timeout, _ := cmd.Flags().GetDuration("timeout")

			cookieHeader, err := refreshCMCookieInteractively(ctx, cmCookieRefreshOptions{
				URL:          url,
				ProfileDir:   profileDir,
				OutPath:      outPath,
				Headed:       headed,
				CloseBrowser: closeBrowser,
				Timeout:      timeout,
				Prompt:       true,
			})
			if err != nil {
				return err
			}

			if outPath != "" {
				fmt.Fprintf(os.Stderr, "Wrote cookie to %s\n", outPath)
				fmt.Fprintf(os.Stderr, "Use it with: aads-aso popscore --cookie-file %s ...\n", outPath)
				return nil
			}
			fmt.Fprintln(os.Stdout, cookieHeader)
			return nil
		},
	}

	cmd.Flags().String("url", "https://app-ads.apple.com/", "URL to open (Apple Ads web)")
	cmd.Flags().Bool("headed", true, "Open the browser in headed mode")
	cmd.Flags().String("profile-dir", "", "Playwright persistent profile directory (defaults to ~/.aads/playwright-app-ads-profile)")
	cmd.Flags().String("out", "", "Write cookie header value to this file (0600). If empty, prints to stdout.")
	cmd.Flags().Bool("close", true, "Close the browser after exporting cookies")
	cmd.Flags().Duration("timeout", 2*time.Minute, "Max time for cookie extraction after you press Enter")

	return cmd
}

func defaultCMCookieProfileDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ".aads_playwright_app_ads_profile"
	}
	return filepath.Join(home, ".aads", "playwright-app-ads-profile")
}

func defaultCMCookieFilePath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ".aads_app_ads_cookie.txt"
	}
	return filepath.Join(home, ".aads", "app_ads_cookie.txt")
}

type cmCookieRefreshOptions struct {
	URL          string
	ProfileDir   string
	OutPath      string
	Headed       bool
	CloseBrowser bool
	Timeout      time.Duration
	Prompt       bool
}

func refreshCMCookieInteractively(ctx context.Context, opts cmCookieRefreshOptions) (string, error) {
	url := strings.TrimSpace(opts.URL)
	if url == "" {
		url = "https://app-ads.apple.com/"
	}
	profileDir := strings.TrimSpace(opts.ProfileDir)
	if profileDir == "" {
		profileDir = defaultCMCookieProfileDir()
	}

	// Ensure profile dir exists.
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return "", fmt.Errorf("create profile dir: %w", err)
	}

	// Unique session name so this doesn't clash with other Playwright CLI usage.
	session := newCMCookieSessionName()

	// 1) Open browser to Apple Ads.
	openArgs := []string{"--session", session, "open", url, "--persistent", "--profile", profileDir}
	if opts.Headed {
		openArgs = append(openArgs, "--headed")
	}

	if _, err := runPlaywrightCLI(ctx, openArgs...); err != nil {
		// Fallback when persistent Chrome profile is already in use by another process.
		// We can still refresh cookie in a non-persistent browser context and save it to file.
		if isPersistentBrowserInUseErr(err) {
			fmt.Fprintln(os.Stderr, "Persistent browser profile is busy; retrying with a temporary browser context...")
			openArgs = []string{"--session", session, "open", url}
			if opts.Headed {
				openArgs = append(openArgs, "--headed")
			}
			if _, err2 := runPlaywrightCLI(ctx, openArgs...); err2 != nil {
				return "", err2
			}
		} else {
			return "", err
		}
	}

	// 2) Wait for user to complete login.
	if opts.Prompt {
		fmt.Fprintln(os.Stderr, "Browser opened. Complete Apple Ads login in the browser window.")
		fmt.Fprintln(os.Stderr, "When you are logged in, press Enter here to export cookies...")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}

	// 3) Extract cookie header string from the browser context.
	extractFn := "async (page) => {\n" +
		"  const cookies = await page.context().cookies('https://app-ads.apple.com');\n" +
		"  return cookies.map(c => `${c.name}=${c.value}`).join('; ');\n" +
		"}"

	extractCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		extractCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	out, err := runPlaywrightCLI(extractCtx, "--session", session, "run-code", extractFn)
	if err != nil {
		return "", err
	}

	cookieHeader, err := parsePWCLIResultString(out)
	if err != nil {
		return "", err
	}
	cookieHeader = strings.TrimSpace(cookieHeader)
	if cookieHeader == "" {
		return "", fmt.Errorf("exported cookie is empty; are you logged in to app-ads.apple.com in the opened browser?")
	}

	// 4) Optionally close the browser.
	if opts.CloseBrowser {
		_, _ = runPlaywrightCLI(ctx, "--session", session, "close")
	}

	if strings.TrimSpace(opts.OutPath) != "" {
		if err := os.MkdirAll(filepath.Dir(opts.OutPath), 0o700); err != nil {
			return "", fmt.Errorf("create cookie file dir: %w", err)
		}
		if err := os.WriteFile(opts.OutPath, []byte(cookieHeader+"\n"), 0o600); err != nil {
			return "", fmt.Errorf("write cookie file: %w", err)
		}
	}

	return cookieHeader, nil
}

func newCMCookieSessionName() string {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err == nil {
		return fmt.Sprintf("aads-cm-cookie-%d-%d-%s", time.Now().UnixNano(), os.Getpid(), hex.EncodeToString(suffix[:]))
	}
	return fmt.Sprintf("aads-cm-cookie-%d-%d", time.Now().UnixNano(), os.Getpid())
}

func isPersistentBrowserInUseErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "launchpersistentcontext") ||
		strings.Contains(s, "opening in existing browser session")
}

func runPlaywrightCLI(ctx context.Context, args ...string) ([]byte, error) {
	// Prefer the Codex-installed wrapper script if present; otherwise fall back to npx directly.
	codexHome := os.Getenv("CODEX_HOME")
	if strings.TrimSpace(codexHome) == "" {
		home, _ := os.UserHomeDir()
		if strings.TrimSpace(home) != "" {
			codexHome = filepath.Join(home, ".codex")
		}
	}

	wrapper := ""
	if strings.TrimSpace(codexHome) != "" {
		wrapper = filepath.Join(codexHome, "skills", "playwright", "scripts", "playwright_cli.sh")
		if fi, err := os.Stat(wrapper); err == nil && !fi.IsDir() {
			wd, _ := os.Getwd()
			cmd := exec.CommandContext(ctx, "bash", append([]string{wrapper}, args...)...)
			cmd.Env = os.Environ()
			if strings.TrimSpace(wd) != "" {
				cmd.Dir = wd
			}
			b, err := cmd.CombinedOutput()
			if err != nil {
				return nil, fmt.Errorf("playwright-cli (wrapper) failed: %w\n%s", err, strings.TrimSpace(string(b)))
			}
			return b, nil
		}
	}

	// Fallback: npx --package @playwright/cli playwright-cli ...
	wd, _ := os.Getwd()
	cmd := exec.CommandContext(ctx, "npx", append([]string{"--yes", "--package", "@playwright/cli", "playwright-cli"}, args...)...)
	cmd.Env = os.Environ()
	if strings.TrimSpace(wd) != "" {
		cmd.Dir = wd
	}
	b, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("playwright-cli (npx) failed: %w\n%s", err, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func parsePWCLIResultString(out []byte) (string, error) {
	// Playwright CLI prints:
	//   ### Result
	//   "..."
	// We parse the JSON value immediately following the "### Result" marker.
	lines := strings.Split(string(out), "\n")
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "### Result" {
			// Find first non-empty line after marker.
			for j := i + 1; j < len(lines); j++ {
				s := strings.TrimSpace(lines[j])
				if s == "" {
					continue
				}
				if strings.HasPrefix(s, "###") {
					return "", fmt.Errorf("playwright-cli output missing result value after ### Result")
				}
				var v string
				if err := json.Unmarshal([]byte(s), &v); err != nil {
					return "", fmt.Errorf("parse playwright-cli result as string: %w (line=%q)", err, s)
				}
				return v, nil
			}
			return "", fmt.Errorf("playwright-cli output ended after ### Result")
		}
	}
	return "", fmt.Errorf("playwright-cli output missing ### Result marker")
}
