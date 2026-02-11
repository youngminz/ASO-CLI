# aads-aso-cli

Standalone CLI for ASO workflows that rely on unofficial Apple endpoints.

## Build

```bash
go build -o /tmp/aads-aso ./cmd/aads-aso
```

## Commands

### `hints`

Fetch App Store autocomplete suggestions via `MZSearchHints` (no Apple Ads login required).

```bash
/tmp/aads-aso hints \
  --countries US,GB \
  --query "plant id" \
  --limit 10 \
  --output table
```

### `popscore`

Fetch keyword popularity (usually `1-100`) from Apple Ads web APIs.

```bash
/tmp/aads-aso popscore \
  --countries US \
  --app-url "https://apps.apple.com/us/app/your-app/id1234567890" \
  --keywords "plant identifier,plant app" \
  --cookie-file "$HOME/.aads/app_ads_cookie.txt" \
  --output table
```

### `recommend`

Fetch related keyword recommendations from Apple Ads web APIs.

```bash
/tmp/aads-aso recommend \
  --countries US \
  --bundle-id "com.example.app" \
  --text "plant identifier" \
  --limit 25 \
  --min-popularity 20 \
  --cookie-file "$HOME/.aads/app_ads_cookie.txt" \
  --output json
```

### Adam ID Auto-Resolution

For `popscore` and `recommend`, you can still pass `--adam-id`, but it is no longer required if you provide one of:

- `--app-url` (extracts `adam-id` directly from App Store URL)
- `--bundle-id` (resolves via iTunes Lookup API)
- `--app-name` (resolves via iTunes Search API)

Optional:

- `--adam-country` to control the country used for lookup/search (defaults to first `--countries` value).
- If no `adam-id` input is provided, the CLI auto-selects an owned `adam-id` from your authenticated Apple Ads campaigns.
- If a provided `adam-id` is not owned by your Apple Ads account (`NO_USER_OWNED_APPS_FOUND_CODE`), the CLI auto-falls back to an owned `adam-id` and retries once.

### `cm-cookie`

Interactive helper that opens a real browser and exports a cookie header for `app-ads.apple.com`.

```bash
/tmp/aads-aso cm-cookie \
  --out "$HOME/.aads/app_ads_cookie.txt" \
  --headed
```

## Auth and Cookie Behavior

- `popscore` and `recommend` require an authenticated Apple Ads session cookie.
- `--cookie-file` defaults to `~/.aads/app_ads_cookie.txt`.

### Option A: Automated Browser-Assisted Auth (Playwright)

You can automate cookie collection with Playwright (CLI or MCP), either via `cm-cookie` or by leaving `--auto-cookie=true` on `popscore`/`recommend`.

1. Command opens a real browser using Playwright.
2. You must complete Apple Ads login and 2FA manually (this is not bypassed).
3. If keyword calls keep returning auth/refresh errors, go to an existing ad group and open **Add Keywords** once, then retry cookie capture.
4. Return to terminal and press Enter when prompted to export cookies.

Example:

```bash
/tmp/aads-aso cm-cookie --out "$HOME/.aads/app_ads_cookie.txt" --headed
```

### Option B: Manual Cookie Capture

You can also capture cookies manually from your logged-in Apple Ads browser session.

1. Open DevTools Network tab on `app-ads.apple.com`.
2. Find a request to `/cm/api/v2/keywords/popularities` or `/cm/api/v2/keywords/recommendation`.
3. Copy the `Cookie` header value (header value only, not the `Cookie:` prefix).
4. Save it to `~/.aads/app_ads_cookie.txt` or pass it with `--cookie`.

Notes:

- The CLI automatically maps `XSRF-TOKEN-CM` cookie to `X-XSRF-TOKEN-CM` header when present.
- Use `--header "Name: value"` for any extra headers your session requires.
- `NO_USER_OWNED_APPS_FOUND_CODE` means authentication worked, but the selected app is not owned/accessible by the logged-in Apple Ads account.

## Output Formats

Global flag:

```bash
--output json|table|yaml
```

Default output format is `json`.

## Testing

Automated:

```bash
go test ./...
```

Current status: package builds and tests run, but there are no `_test.go` files yet.

Smoke tests:

```bash
go build -o /tmp/aads-aso ./cmd/aads-aso
/tmp/aads-aso --help
/tmp/aads-aso cm-cookie --help
/tmp/aads-aso hints --countries US --query "plant" --limit 5 --output table
```

For `popscore` and `recommend`, run with a valid cookie file (or keep `--auto-cookie=true` to refresh interactively).

## Warning

All endpoints used by this CLI are unofficial and may change or break without notice.
