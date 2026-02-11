package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	itunesLookupURL    = "https://itunes.apple.com/lookup"
	itunesSearchURL    = "https://itunes.apple.com/search"
	itunesHTTPTO       = 15 * time.Second
	defaultAdamCountry = "US"
)

var appStoreIDPattern = regexp.MustCompile(`id([0-9]{5,})`)

var errAdamIDNotProvided = errors.New("adam-id not provided")

type itunesAPIResponse struct {
	ResultCount int              `json:"resultCount"`
	Results     []itunesAppEntry `json:"results"`
}

type itunesAppEntry struct {
	TrackID   int64  `json:"trackId"`
	TrackName string `json:"trackName"`
	BundleID  string `json:"bundleId"`
}

func resolveAdamIDFromFlags(ctx context.Context, cmd *cobra.Command, countries []string) (int64, error) {
	adamID, _ := cmd.Flags().GetInt64("adam-id")
	if adamID > 0 {
		return adamID, nil
	}

	appURL, _ := cmd.Flags().GetString("app-url")
	if strings.TrimSpace(appURL) != "" {
		id, err := parseAdamIDFromAppURL(appURL)
		if err != nil {
			return 0, fmt.Errorf("parse --app-url: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Resolved adam-id=%d from --app-url\n", id)
		return id, nil
	}

	lookupCountry := adamLookupCountry(cmd, countries)

	bundleID, _ := cmd.Flags().GetString("bundle-id")
	bundleID = strings.TrimSpace(bundleID)
	if bundleID != "" {
		id, appName, err := lookupAdamIDByBundleID(ctx, bundleID, lookupCountry)
		if err != nil {
			return 0, fmt.Errorf("resolve from --bundle-id: %w", err)
		}
		if appName != "" {
			fmt.Fprintf(os.Stderr, "Resolved adam-id=%d from bundle-id %q (%s)\n", id, bundleID, appName)
		} else {
			fmt.Fprintf(os.Stderr, "Resolved adam-id=%d from bundle-id %q\n", id, bundleID)
		}
		return id, nil
	}

	appName, _ := cmd.Flags().GetString("app-name")
	appName = strings.TrimSpace(appName)
	if appName != "" {
		id, resolvedName, resolvedBundleID, err := searchAdamIDByAppName(ctx, appName, lookupCountry)
		if err != nil {
			return 0, fmt.Errorf("resolve from --app-name: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Resolved adam-id=%d from app-name %q -> %q (%s)\n", id, appName, resolvedName, resolvedBundleID)
		return id, nil
	}

	return 0, fmt.Errorf("%w: --adam-id is required (or provide --app-url, --bundle-id, or --app-name)", errAdamIDNotProvided)
}

func adamLookupCountry(cmd *cobra.Command, countries []string) string {
	cc, _ := cmd.Flags().GetString("adam-country")
	cc = strings.ToUpper(strings.TrimSpace(cc))
	if cc != "" {
		return cc
	}
	if len(countries) > 0 && strings.TrimSpace(countries[0]) != "" {
		return strings.ToUpper(strings.TrimSpace(countries[0]))
	}
	return defaultAdamCountry
}

func parseAdamIDFromAppURL(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, fmt.Errorf("empty value")
	}

	// Allow users to pass a raw numeric value to the --app-url flag.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		return n, nil
	}

	// Accept scheme-less App Store links.
	if !strings.Contains(s, "://") {
		s = "https://" + strings.TrimLeft(s, "/")
	}

	u, err := url.Parse(s)
	if err != nil {
		return 0, fmt.Errorf("invalid URL: %w", err)
	}

	if id := parseAdamIDFromText(u.Path); id > 0 {
		return id, nil
	}
	if id := parseAdamIDFromText(u.RawPath); id > 0 {
		return id, nil
	}
	if qid := strings.TrimSpace(u.Query().Get("id")); qid != "" {
		n, err := strconv.ParseInt(qid, 10, 64)
		if err == nil && n > 0 {
			return n, nil
		}
	}
	if id := parseAdamIDFromText(raw); id > 0 {
		return id, nil
	}
	return 0, fmt.Errorf("could not find adam-id in %q", raw)
}

func parseAdamIDFromText(s string) int64 {
	m := appStoreIDPattern.FindStringSubmatch(s)
	if len(m) < 2 {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(m[1]), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func lookupAdamIDByBundleID(ctx context.Context, bundleID, country string) (int64, string, error) {
	q := url.Values{}
	q.Set("bundleId", bundleID)
	q.Set("country", strings.ToLower(strings.TrimSpace(country)))

	var resp itunesAPIResponse
	if err := itunesGetJSON(ctx, itunesLookupURL, q, &resp); err != nil {
		return 0, "", err
	}
	if len(resp.Results) == 0 {
		return 0, "", fmt.Errorf("no App Store result for bundle-id %q in country %s", bundleID, strings.ToUpper(country))
	}

	normalizedBundleID := strings.ToLower(strings.TrimSpace(bundleID))
	for _, it := range resp.Results {
		if it.TrackID <= 0 {
			continue
		}
		if strings.ToLower(strings.TrimSpace(it.BundleID)) == normalizedBundleID {
			return it.TrackID, strings.TrimSpace(it.TrackName), nil
		}
	}

	for _, it := range resp.Results {
		if it.TrackID > 0 {
			return it.TrackID, strings.TrimSpace(it.TrackName), nil
		}
	}

	return 0, "", fmt.Errorf("no valid adam-id found for bundle-id %q", bundleID)
}

func searchAdamIDByAppName(ctx context.Context, appName, country string) (int64, string, string, error) {
	q := url.Values{}
	q.Set("term", appName)
	q.Set("entity", "software")
	q.Set("limit", "10")
	q.Set("country", strings.ToLower(strings.TrimSpace(country)))

	var resp itunesAPIResponse
	if err := itunesGetJSON(ctx, itunesSearchURL, q, &resp); err != nil {
		return 0, "", "", err
	}
	if len(resp.Results) == 0 {
		return 0, "", "", fmt.Errorf("no App Store result for app-name %q in country %s", appName, strings.ToUpper(country))
	}

	target := strings.ToLower(strings.TrimSpace(appName))
	for _, it := range resp.Results {
		if it.TrackID <= 0 {
			continue
		}
		if strings.ToLower(strings.TrimSpace(it.TrackName)) == target {
			return it.TrackID, strings.TrimSpace(it.TrackName), strings.TrimSpace(it.BundleID), nil
		}
	}

	for _, it := range resp.Results {
		if it.TrackID > 0 {
			return it.TrackID, strings.TrimSpace(it.TrackName), strings.TrimSpace(it.BundleID), nil
		}
	}

	return 0, "", "", fmt.Errorf("no valid adam-id found for app-name %q", appName)
}

func itunesGetJSON(ctx context.Context, endpoint string, q url.Values, out any) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: itunesHTTPTO}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("itunes lookup HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode itunes response: %w", err)
	}
	return nil
}
