package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const cmAPIBase = "https://app-ads.apple.com/cm/api/v2"

type cmKeywordItem struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Popularity int    `json:"popularity"`
	MatchType  string `json:"matchType"`
}

type cmSuccessResponse struct {
	RequestID string          `json:"requestID"`
	Status    string          `json:"status"`
	Data      []cmKeywordItem `json:"data"`
}

type cmCampaignItem struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	AdamID int64  `json:"adamId"`
}

type cmCampaignFindResponse struct {
	RequestID string           `json:"requestID"`
	Status    string           `json:"status"`
	Data      []cmCampaignItem `json:"data"`
}

type cmErrorResponse struct {
	ErrorMsg          string `json:"errorMsg"`
	ErrorCode         string `json:"errorCode"`
	InternalErrorCode string `json:"internalErrorCode"`
}

type cmErrorNestedResponse struct {
	RequestID string `json:"requestID"`
	Status    string `json:"status"`
	Error     struct {
		Errors []struct {
			MessageCode string `json:"messageCode"`
			Message     string `json:"message"`
			Field       string `json:"field"`
		} `json:"errors"`
	} `json:"error"`
}

type asoPopscoreRow struct {
	Keyword    string  `json:"keyword"`
	Country    string  `json:"country"`
	Popularity *int    `json:"popularity,omitempty"`
	MatchType  *string `json:"matchType,omitempty"`
	Found      bool    `json:"found"`
	Source     string  `json:"source"`
}

type asoRecommendRow struct {
	Country    string  `json:"country"`
	Seed       string  `json:"seed"`
	Term       string  `json:"term"`
	Popularity *int    `json:"popularity,omitempty"`
	MatchType  *string `json:"matchType,omitempty"`
	Rank       int     `json:"rank"`
	Source     string  `json:"source"`
}

func newASOPopscoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "popscore",
		Short: "Keyword popularity score (1-100) via Apple Ads web endpoint (requires session cookie)",
		Long: "Fetch keyword popularity scores (typically 1-100) using an undocumented Apple Ads web endpoint.\n" +
			"Requires a valid Cookie header from an authenticated app-ads.apple.com session.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			countries, err := getCountriesFlag(cmd)
			if err != nil {
				return err
			}

			keywords, err := getKeywordsFlags(cmd)
			if err != nil {
				return err
			}
			if len(keywords) == 0 {
				return fmt.Errorf("no keywords provided (use --keywords or --keywords-file)")
			}

			cookie, err := getCookieFlag(ctx, cmd)
			if err != nil {
				return err
			}

			extraHeaders, err := getExtraHeaders(cmd)
			if err != nil {
				return err
			}
			autoCookie, _ := cmd.Flags().GetBool("auto-cookie")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			adamID, cookie, err := resolveAdamIDForCMCommand(ctx, cmd, countries, cookie, extraHeaders, autoCookie, timeout)
			if err != nil {
				return err
			}

			callPopularities := func(cookieValue, country string) ([]cmKeywordItem, error) {
				reqCtx, cancel := withOptionalTimeout(ctx, timeout)
				defer cancel()
				return cmKeywordPopularities(reqCtx, cookieValue, extraHeaders, adamID, country, keywords)
			}

			var out []asoPopscoreRow
			attemptedOwnedAdamFallback := false
			for _, cc := range countries {
				respItems, err := callPopularities(cookie, cc)
				if err != nil && autoCookie && isCMRefreshError(err) {
					fmt.Fprintln(os.Stderr, "Cookie appears expired. Launching browser to refresh session...")
					cookie, err = refreshCMCookieFromFlags(ctx, cmd)
					if err != nil {
						return err
					}
					respItems, err = callPopularities(cookie, cc)
				}
				if err != nil && !attemptedOwnedAdamFallback && isCMNoUserOwnedAppsError(err) {
					attemptedOwnedAdamFallback = true
					ownedAdamID, updatedCookie, discoverErr := discoverOwnedAdamIDWithRefresh(ctx, cmd, cookie, extraHeaders, autoCookie, timeout)
					if discoverErr != nil {
						return fmt.Errorf("adam-id %d is not accessible for this Apple Ads account, and auto-discovery failed: %w", adamID, discoverErr)
					}
					if ownedAdamID > 0 && ownedAdamID != adamID {
						fmt.Fprintf(os.Stderr, "adam-id %d is not owned by this account; switching to owned adam-id %d and retrying...\n", adamID, ownedAdamID)
						adamID = ownedAdamID
					}
					cookie = updatedCookie
					respItems, err = callPopularities(cookie, cc)
				}
				if err != nil {
					return err
				}

				byName := map[string]cmKeywordItem{}
				for _, it := range respItems {
					byName[normKeyword(it.Name)] = it
				}

				for _, kw := range keywords {
					it, ok := byName[normKeyword(kw)]
					row := asoPopscoreRow{
						Keyword: kw,
						Country: cc,
						Found:   ok,
						Source:  "cm_api_v2",
					}
					if ok {
						pop := it.Popularity
						mt := strings.TrimSpace(it.MatchType)
						row.Popularity = &pop
						if mt != "" {
							row.MatchType = &mt
						}
					}
					out = append(out, row)
				}
			}

			return printOutput(out)
		},
	}

	addCommonCMKeywordFlags(cmd)
	cmd.Flags().String("keywords", "", "Comma-separated keywords")
	cmd.Flags().String("keywords-file", "", "Path to file with one keyword per line")
	return cmd
}

func newASORecommendCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recommend",
		Short: "Related keyword recommendations via Apple Ads web endpoint (requires session cookie)",
		Long: "Fetch related keyword recommendations using an undocumented Apple Ads web endpoint.\n" +
			"Requires a valid Cookie header from an authenticated app-ads.apple.com session.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			countries, err := getCountriesFlag(cmd)
			if err != nil {
				return err
			}

			seed, _ := cmd.Flags().GetString("text")
			seed = strings.TrimSpace(seed)
			if seed == "" {
				return fmt.Errorf("--text is required")
			}

			cookie, err := getCookieFlag(ctx, cmd)
			if err != nil {
				return err
			}

			extraHeaders, err := getExtraHeaders(cmd)
			if err != nil {
				return err
			}
			autoCookie, _ := cmd.Flags().GetBool("auto-cookie")
			timeout, _ := cmd.Flags().GetDuration("timeout")
			adamID, cookie, err := resolveAdamIDForCMCommand(ctx, cmd, countries, cookie, extraHeaders, autoCookie, timeout)
			if err != nil {
				return err
			}

			limit, _ := cmd.Flags().GetInt("limit")
			if limit <= 0 {
				limit = 50
			}
			minPop, _ := cmd.Flags().GetInt("min-popularity")

			callRecommendations := func(cookieValue, country string) ([]cmKeywordItem, error) {
				reqCtx, cancel := withOptionalTimeout(ctx, timeout)
				defer cancel()
				return cmKeywordRecommendation(reqCtx, cookieValue, extraHeaders, adamID, country, seed)
			}

			var out []asoRecommendRow
			attemptedOwnedAdamFallback := false
			for _, cc := range countries {
				items, err := callRecommendations(cookie, cc)
				if err != nil && autoCookie && isCMRefreshError(err) {
					fmt.Fprintln(os.Stderr, "Cookie appears expired. Launching browser to refresh session...")
					cookie, err = refreshCMCookieFromFlags(ctx, cmd)
					if err != nil {
						return err
					}
					items, err = callRecommendations(cookie, cc)
				}
				if err != nil && !attemptedOwnedAdamFallback && isCMNoUserOwnedAppsError(err) {
					attemptedOwnedAdamFallback = true
					ownedAdamID, updatedCookie, discoverErr := discoverOwnedAdamIDWithRefresh(ctx, cmd, cookie, extraHeaders, autoCookie, timeout)
					if discoverErr != nil {
						return fmt.Errorf("adam-id %d is not accessible for this Apple Ads account, and auto-discovery failed: %w", adamID, discoverErr)
					}
					if ownedAdamID > 0 && ownedAdamID != adamID {
						fmt.Fprintf(os.Stderr, "adam-id %d is not owned by this account; switching to owned adam-id %d and retrying...\n", adamID, ownedAdamID)
						adamID = ownedAdamID
					}
					cookie = updatedCookie
					items, err = callRecommendations(cookie, cc)
				}
				if err != nil {
					return err
				}

				var kept []cmKeywordItem
				for _, it := range items {
					if it.Popularity < minPop {
						continue
					}
					if strings.TrimSpace(it.Name) == "" {
						continue
					}
					kept = append(kept, it)
				}
				sort.Slice(kept, func(i, j int) bool {
					if kept[i].Popularity != kept[j].Popularity {
						return kept[i].Popularity > kept[j].Popularity
					}
					return strings.ToLower(kept[i].Name) < strings.ToLower(kept[j].Name)
				})
				if len(kept) > limit {
					kept = kept[:limit]
				}

				for i, it := range kept {
					pop := it.Popularity
					mt := strings.TrimSpace(it.MatchType)
					var mtPtr *string
					if mt != "" {
						mtPtr = &mt
					}
					out = append(out, asoRecommendRow{
						Country:    cc,
						Seed:       seed,
						Term:       it.Name,
						Popularity: &pop,
						MatchType:  mtPtr,
						Rank:       i + 1,
						Source:     "cm_api_v2",
					})
				}
			}

			return printOutput(out)
		},
	}

	addCommonCMKeywordFlags(cmd)
	cmd.Flags().String("text", "", "Seed text to get related keyword recommendations")
	_ = cmd.MarkFlagRequired("text")
	cmd.Flags().Int("limit", 50, "Max recommendations per country")
	cmd.Flags().Int("min-popularity", 0, "Minimum popularity score (typically 1-100)")
	return cmd
}

func addCommonCMKeywordFlags(cmd *cobra.Command) {
	cmd.Flags().String("countries", "", "Comma-separated country codes (alpha-2), e.g. US,GB")
	_ = cmd.MarkFlagRequired("countries")
	cmd.Flags().Int64("adam-id", 0, "App Store app adamId (optional when auto-resolving from other app flags)")
	cmd.Flags().String("app-url", "", "App Store URL (extracts adamId automatically)")
	cmd.Flags().String("bundle-id", "", "Bundle ID to auto-resolve adamId via iTunes Lookup")
	cmd.Flags().String("app-name", "", "App name to auto-resolve adamId via iTunes Search")
	cmd.Flags().String("adam-country", "", "Country for adamId lookup/search (defaults to first --countries value)")
	addCookieFlags(cmd)
	addExtraHeaderFlags(cmd)
	cmd.Flags().Duration("timeout", 30*time.Second, "Request timeout per country")
}

func addCookieFlags(cmd *cobra.Command) {
	cmd.Flags().String("cookie", "", "Cookie header value (e.g. 'a=b; c=d') from an authenticated app-ads.apple.com session")
	cmd.Flags().String("cookie-file", defaultCMCookieFilePath(), "Path to file containing Cookie header value (also used as cache when --auto-cookie is enabled)")
	cmd.Flags().Bool("auto-cookie", true, "If cookie is missing/expired, open Playwright for interactive refresh")
	cmd.Flags().String("cookie-profile-dir", "", "Playwright persistent profile directory for cookie refresh")
}

func addExtraHeaderFlags(cmd *cobra.Command) {
	cmd.Flags().StringArray("header", nil, "Extra request header 'Name: value' (repeatable)")
}

func getCookieFlag(ctx context.Context, cmd *cobra.Command) (string, error) {
	cookie, _ := cmd.Flags().GetString("cookie")
	cookieFile, _ := cmd.Flags().GetString("cookie-file")
	autoCookie, _ := cmd.Flags().GetBool("auto-cookie")

	cookie = strings.TrimSpace(cookie)
	if cookie == "" && strings.TrimSpace(cookieFile) != "" {
		b, err := os.ReadFile(cookieFile)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		} else {
			cookie = strings.TrimSpace(string(b))
		}
	}

	if strings.HasPrefix(strings.ToLower(cookie), "cookie:") {
		cookie = strings.TrimSpace(cookie[len("cookie:"):])
	}

	if cookie == "" {
		if !autoCookie {
			return "", fmt.Errorf("--cookie (or --cookie-file) is required for this command")
		}
		fmt.Fprintln(os.Stderr, "Cookie not found. Launching browser to refresh session...")
		return refreshCMCookieFromFlags(ctx, cmd)
	}
	return cookie, nil
}

func refreshCMCookieFromFlags(ctx context.Context, cmd *cobra.Command) (string, error) {
	profileDir, _ := cmd.Flags().GetString("cookie-profile-dir")
	cookieFile, _ := cmd.Flags().GetString("cookie-file")
	if strings.TrimSpace(cookieFile) == "" {
		cookieFile = defaultCMCookieFilePath()
	}

	return refreshCMCookieInteractively(ctx, cmCookieRefreshOptions{
		URL:          "https://app-ads.apple.com/",
		ProfileDir:   profileDir,
		OutPath:      cookieFile,
		Headed:       true,
		CloseBrowser: true,
		Timeout:      2 * time.Minute,
		Prompt:       true,
	})
}

func getExtraHeaders(cmd *cobra.Command) (map[string]string, error) {
	raw, _ := cmd.Flags().GetStringArray("header")
	if len(raw) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, h := range raw {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		i := strings.IndexByte(h, ':')
		if i <= 0 {
			return nil, fmt.Errorf("invalid --header %q (expected 'Name: value')", h)
		}
		name := strings.TrimSpace(h[:i])
		val := strings.TrimSpace(h[i+1:])
		if name == "" {
			return nil, fmt.Errorf("invalid --header %q (empty name)", h)
		}
		out[name] = val
	}
	return out, nil
}

func resolveAdamIDForCMCommand(
	ctx context.Context,
	cmd *cobra.Command,
	countries []string,
	cookie string,
	extraHeaders map[string]string,
	autoCookie bool,
	timeout time.Duration,
) (int64, string, error) {
	adamID, err := resolveAdamIDFromFlags(ctx, cmd, countries)
	if err == nil {
		return adamID, cookie, nil
	}
	if !errors.Is(err, errAdamIDNotProvided) {
		return 0, cookie, err
	}

	ownedAdamID, updatedCookie, discoverErr := discoverOwnedAdamIDWithRefresh(ctx, cmd, cookie, extraHeaders, autoCookie, timeout)
	if discoverErr != nil {
		return 0, cookie, fmt.Errorf("auto-resolve adam-id from Apple Ads account: %w", discoverErr)
	}
	fmt.Fprintf(os.Stderr, "Resolved adam-id=%d from Apple Ads owned campaigns\n", ownedAdamID)
	return ownedAdamID, updatedCookie, nil
}

func discoverOwnedAdamIDWithRefresh(
	ctx context.Context,
	cmd *cobra.Command,
	cookie string,
	extraHeaders map[string]string,
	autoCookie bool,
	timeout time.Duration,
) (int64, string, error) {
	discover := func(cookieValue string) (int64, error) {
		reqCtx, cancel := withOptionalTimeout(ctx, timeout)
		defer cancel()
		adamID, campaignName, err := cmDiscoverOwnedAdamID(reqCtx, cookieValue, extraHeaders)
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(campaignName) != "" {
			fmt.Fprintf(os.Stderr, "Selected owned adam-id=%d from campaign %q\n", adamID, campaignName)
		}
		return adamID, nil
	}

	adamID, err := discover(cookie)
	if err != nil && autoCookie && isCMRefreshError(err) {
		fmt.Fprintln(os.Stderr, "Cookie appears expired while discovering owned apps. Launching browser to refresh session...")
		cookie, err = refreshCMCookieFromFlags(ctx, cmd)
		if err != nil {
			return 0, cookie, err
		}
		adamID, err = discover(cookie)
	}
	if err != nil {
		return 0, cookie, err
	}
	return adamID, cookie, nil
}

func cmKeywordPopularities(
	ctx context.Context,
	cookie string,
	extraHeaders map[string]string,
	adamID int64,
	storefront string,
	terms []string,
) ([]cmKeywordItem, error) {
	u, _ := url.Parse(cmAPIBase + "/keywords/popularities")
	q := u.Query()
	q.Set("adamId", strconv.FormatInt(adamID, 10))
	u.RawQuery = q.Encode()

	reqBody := map[string]any{
		"storefronts": []string{strings.ToUpper(strings.TrimSpace(storefront))},
		"terms":       terms,
	}

	b, err := cmPostJSON(ctx, u.String(), reqBody, cookie, extraHeaders)
	if err != nil {
		return nil, err
	}
	return parseCMKeywordData("popularities", b)
}

func cmKeywordRecommendation(
	ctx context.Context,
	cookie string,
	extraHeaders map[string]string,
	adamID int64,
	storefront string,
	text string,
) ([]cmKeywordItem, error) {
	u, _ := url.Parse(cmAPIBase + "/keywords/recommendation")
	q := u.Query()
	q.Set("adamId", strconv.FormatInt(adamID, 10))
	q.Set("text", text)
	u.RawQuery = q.Encode()

	reqBody := map[string]any{
		"storefronts": []string{strings.ToUpper(strings.TrimSpace(storefront))},
	}

	b, err := cmPostJSON(ctx, u.String(), reqBody, cookie, extraHeaders)
	if err != nil {
		return nil, err
	}
	return parseCMKeywordData("recommendation", b)
}

func cmDiscoverOwnedAdamID(
	ctx context.Context,
	cookie string,
	extraHeaders map[string]string,
) (int64, string, error) {
	campaigns, err := cmCampaignsFind(ctx, cookie, extraHeaders)
	if err != nil {
		return 0, "", err
	}
	seen := map[int64]bool{}
	for _, it := range campaigns {
		if it.AdamID <= 0 || seen[it.AdamID] {
			continue
		}
		seen[it.AdamID] = true
		return it.AdamID, strings.TrimSpace(it.Name), nil
	}
	return 0, "", fmt.Errorf("no owned adam-id found in Apple Ads campaigns")
}

func cmCampaignsFind(
	ctx context.Context,
	cookie string,
	extraHeaders map[string]string,
) ([]cmCampaignItem, error) {
	b, err := cmGetJSON(ctx, cmAPIBase+"/campaigns/find", cookie, extraHeaders)
	if err != nil {
		return nil, err
	}
	return parseCMCampaignData("campaigns/find", b)
}

func parseCMCampaignData(endpoint string, body []byte) ([]cmCampaignItem, error) {
	var ok cmCampaignFindResponse
	if err := json.Unmarshal(body, &ok); err == nil && (ok.Status == "" || strings.EqualFold(ok.Status, "success")) {
		return ok.Data, nil
	}

	var er cmErrorResponse
	if err := json.Unmarshal(body, &er); err == nil && (er.ErrorMsg != "" || er.ErrorCode != "" || er.InternalErrorCode != "") {
		return nil, fmt.Errorf("cm %s error (%s/%s): %s", endpoint, strings.TrimSpace(er.ErrorCode), strings.TrimSpace(er.InternalErrorCode), strings.TrimSpace(er.ErrorMsg))
	}

	var n cmErrorNestedResponse
	if err := json.Unmarshal(body, &n); err == nil && len(n.Error.Errors) > 0 {
		first := n.Error.Errors[0]
		return nil, fmt.Errorf("cm %s error (%s): %s", endpoint, strings.TrimSpace(first.MessageCode), strings.TrimSpace(first.Message))
	}

	return nil, fmt.Errorf("cm %s: unexpected response: %s", endpoint, strings.TrimSpace(string(body)))
}

func parseCMKeywordData(endpoint string, body []byte) ([]cmKeywordItem, error) {
	var ok cmSuccessResponse
	if err := json.Unmarshal(body, &ok); err == nil && (ok.Status == "" || strings.EqualFold(ok.Status, "success")) {
		return ok.Data, nil
	}

	var er cmErrorResponse
	if err := json.Unmarshal(body, &er); err == nil && (er.ErrorMsg != "" || er.ErrorCode != "" || er.InternalErrorCode != "") {
		return nil, fmt.Errorf("cm %s error (%s/%s): %s", endpoint, strings.TrimSpace(er.ErrorCode), strings.TrimSpace(er.InternalErrorCode), strings.TrimSpace(er.ErrorMsg))
	}

	var n cmErrorNestedResponse
	if err := json.Unmarshal(body, &n); err == nil && len(n.Error.Errors) > 0 {
		first := n.Error.Errors[0]
		return nil, fmt.Errorf("cm %s error (%s): %s", endpoint, strings.TrimSpace(first.MessageCode), strings.TrimSpace(first.Message))
	}

	return nil, fmt.Errorf("cm %s: unexpected response: %s", endpoint, strings.TrimSpace(string(body)))
}

func cmGetJSON(
	ctx context.Context,
	url string,
	cookie string,
	extraHeaders map[string]string,
) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Origin", "https://app-ads.apple.com")
	req.Header.Set("Referer", "https://app-ads.apple.com/")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	if token := cookieValue(cookie, "XSRF-TOKEN-CM"); token != "" {
		if !hasHeaderCaseInsensitive(extraHeaders, "X-XSRF-TOKEN-CM") {
			req.Header.Set("X-XSRF-TOKEN-CM", token)
		}
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cm endpoint HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func cmPostJSON(
	ctx context.Context,
	url string,
	body any,
	cookie string,
	extraHeaders map[string]string,
) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Origin", "https://app-ads.apple.com")
	req.Header.Set("Referer", "https://app-ads.apple.com/")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36")

	if token := cookieValue(cookie, "XSRF-TOKEN-CM"); token != "" {
		if !hasHeaderCaseInsensitive(extraHeaders, "X-XSRF-TOKEN-CM") {
			req.Header.Set("X-XSRF-TOKEN-CM", token)
		}
	}

	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cm endpoint HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return b, nil
}

func cookieValue(cookieHeader, key string) string {
	target := strings.ToLower(strings.TrimSpace(key))
	for _, part := range strings.Split(cookieHeader, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		i := strings.IndexByte(part, '=')
		if i <= 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(part[:i]))
		if k == target {
			return strings.TrimSpace(part[i+1:])
		}
	}
	return ""
}

func hasHeaderCaseInsensitive(headers map[string]string, name string) bool {
	target := strings.ToLower(strings.TrimSpace(name))
	for k := range headers {
		if strings.ToLower(strings.TrimSpace(k)) == target {
			return true
		}
	}
	return false
}

func getCountriesFlag(cmd *cobra.Command) ([]string, error) {
	v, _ := cmd.Flags().GetString("countries")
	if strings.TrimSpace(v) == "" {
		return nil, fmt.Errorf("--countries is required")
	}

	parts := strings.Split(v, ",")
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		cc := strings.ToUpper(strings.TrimSpace(p))
		if cc == "" {
			continue
		}
		if seen[cc] {
			continue
		}
		seen[cc] = true
		out = append(out, cc)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid countries in --countries")
	}
	return out, nil
}

func getKeywordsFlags(cmd *cobra.Command) ([]string, error) {
	inline, _ := cmd.Flags().GetString("keywords")
	file, _ := cmd.Flags().GetString("keywords-file")

	var kws []string
	if strings.TrimSpace(inline) != "" {
		for _, p := range strings.Split(inline, ",") {
			kw := strings.TrimSpace(p)
			if kw == "" {
				continue
			}
			kws = append(kws, kw)
		}
	}

	if strings.TrimSpace(file) != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			kw := strings.TrimSpace(line)
			if kw == "" {
				continue
			}
			kws = append(kws, kw)
		}
	}

	seen := map[string]bool{}
	var out []string
	for _, kw := range kws {
		n := normKeyword(kw)
		if n == "" {
			continue
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, strings.TrimSpace(kw))
	}
	return out, nil
}

func normKeyword(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func withOptionalTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func isCMRefreshError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "no_user_owned_apps_found_code") {
		return false
	}
	return strings.Contains(s, "internalerrorcode\":\"refresh") ||
		strings.Contains(s, "user is not logged in") ||
		(strings.Contains(s, "cm endpoint http 403") && !strings.Contains(s, "no_user_owned_apps_found_code"))
}

func isCMNoUserOwnedAppsError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "no_user_owned_apps_found_code")
}
