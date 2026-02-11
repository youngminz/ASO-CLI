package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type asoHintRow struct {
	Country  string `json:"country"`
	Term     string `json:"term"`
	Rank     int    `json:"rank"`
	Priority *int   `json:"priority,omitempty"`
	Source   string `json:"source"`
}

func newASOHintsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hints",
		Short: "App Store autocomplete suggestions via MZSearchHints (undocumented)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			countries, err := getCountriesFlag(cmd)
			if err != nil {
				return err
			}

			query, _ := cmd.Flags().GetString("query")
			query = strings.TrimSpace(query)
			if query == "" {
				return fmt.Errorf("--query is required")
			}

			limit, _ := cmd.Flags().GetInt("limit")
			if limit <= 0 {
				limit = 20
			}

			storefront, _ := cmd.Flags().GetString("storefront")
			storefront = strings.TrimSpace(storefront)
			if storefront == "" {
				storefront = "143441-1,29 t:native"
			}

			clientApp, _ := cmd.Flags().GetString("client-application")
			clientApp = strings.TrimSpace(clientApp)
			if clientApp == "" {
				clientApp = "Software"
			}

			media, _ := cmd.Flags().GetString("media")
			media = strings.TrimSpace(media)
			if media == "" {
				media = "software"
			}

			eFlag, _ := cmd.Flags().GetBool("e")

			var out []asoHintRow
			for _, cc := range countries {
				terms, err := fetchMZSearchHints(ctx, storefront, clientApp, media, cc, query, eFlag)
				if err != nil {
					return err
				}
				if len(terms) > limit {
					terms = terms[:limit]
				}
				for i, it := range terms {
					rank := i + 1
					out = append(out, asoHintRow{
						Country:  cc,
						Term:     it.Term,
						Rank:     rank,
						Priority: it.Priority,
						Source:   "mzsearchhints",
					})
				}
			}

			return printOutput(out)
		},
	}

	cmd.Flags().String("countries", "", "Comma-separated country codes (alpha-2), e.g. US,GB")
	_ = cmd.MarkFlagRequired("countries")
	cmd.Flags().String("query", "", "Prefix query for suggestions")
	_ = cmd.MarkFlagRequired("query")
	cmd.Flags().Int("limit", 20, "Max suggestions per country")
	cmd.Flags().String("storefront", "143441-1,29 t:native", "Value for X-Apple-Store-Front header")
	cmd.Flags().String("client-application", "Software", "clientApplication query param")
	cmd.Flags().String("media", "software", "media query param")
	cmd.Flags().Bool("e", true, "e query param")

	return cmd
}

type mzHintItem struct {
	Term     string
	Priority *int
}

func fetchMZSearchHints(
	ctx context.Context,
	storefront, clientApp, media, country, query string,
	e bool,
) ([]mzHintItem, error) {
	u, _ := url.Parse("https://search.itunes.apple.com/WebObjects/MZSearchHints.woa/wa/hints")
	q := u.Query()
	q.Set("clientApplication", clientApp)
	q.Set("media", media)
	q.Set("cc", strings.ToLower(country))
	q.Set("q", query)
	q.Set("e", strconv.FormatBool(e))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/xml,application/xml,*/*")
	req.Header.Set("X-Apple-Store-Front", storefront)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hints request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("hints read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("hints HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	v, err := parsePListXML(body)
	if err != nil {
		return nil, err
	}

	root, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected plist root type %T", v)
	}
	hints, ok := root["hints"].([]any)
	if !ok {
		// No hints (empty array or missing key).
		return nil, nil
	}

	var out []mzHintItem
	for _, h := range hints {
		m, ok := h.(map[string]any)
		if !ok {
			continue
		}
		term, _ := m["term"].(string)
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		var pri *int
		switch pv := m["priority"].(type) {
		case int:
			pri = &pv
		case int64:
			p := int(pv)
			pri = &p
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(pv)); err == nil {
				pri = &n
			}
		}
		out = append(out, mzHintItem{Term: term, Priority: pri})
	}

	return out, nil
}

func parsePListXML(data []byte) (any, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))

	// Find <plist>, then parse its first child value.
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("plist: missing <plist> root")
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "plist" {
				// Next value element.
				for {
					tok2, err := dec.Token()
					if err != nil {
						return nil, err
					}
					switch t2 := tok2.(type) {
					case xml.StartElement:
						return parsePListValue(dec, t2)
					case xml.EndElement:
						if t2.Name.Local == "plist" {
							return nil, fmt.Errorf("plist: empty")
						}
					}
				}
			}
		}
	}
}

func parsePListValue(dec *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		m := map[string]any{}
		var key string
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "key":
					s, err := readXMLElementText(dec, "key")
					if err != nil {
						return nil, err
					}
					key = s
				default:
					if key == "" {
						// Skip value without a key.
						if err := skipXMLElement(dec, t.Name.Local); err != nil {
							return nil, err
						}
						continue
					}
					v, err := parsePListValue(dec, t)
					if err != nil {
						return nil, err
					}
					m[key] = v
					key = ""
				}
			case xml.EndElement:
				if t.Name.Local == "dict" {
					return m, nil
				}
			}
		}

	case "array":
		var arr []any
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.StartElement:
				v, err := parsePListValue(dec, t)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			case xml.EndElement:
				if t.Name.Local == "array" {
					return arr, nil
				}
			}
		}

	case "string":
		return readXMLElementText(dec, "string")
	case "integer":
		s, err := readXMLElementText(dec, "integer")
		if err != nil {
			return nil, err
		}
		if s == "" {
			return int64(0), nil
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, err
		}
		return n, nil
	case "real":
		s, err := readXMLElementText(dec, "real")
		if err != nil {
			return nil, err
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, err
		}
		return f, nil
	case "true":
		// Empty element (<true/>). Consume until end.
		if err := consumeToEnd(dec, "true"); err != nil {
			return nil, err
		}
		return true, nil
	case "false":
		if err := consumeToEnd(dec, "false"); err != nil {
			return nil, err
		}
		return false, nil
	default:
		// Unknown element; skip.
		if err := skipXMLElement(dec, start.Name.Local); err != nil {
			return nil, err
		}
		return nil, nil
	}
}

func readXMLElementText(dec *xml.Decoder, endName string) (string, error) {
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write([]byte(t))
		case xml.EndElement:
			if t.Name.Local == endName {
				return b.String(), nil
			}
		case xml.StartElement:
			// Nested markup inside string/key is unexpected; skip it.
			if err := skipXMLElement(dec, t.Name.Local); err != nil {
				return "", err
			}
		}
	}
}

func consumeToEnd(dec *xml.Decoder, endName string) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if t, ok := tok.(xml.EndElement); ok && t.Name.Local == endName {
			return nil
		}
	}
}

func skipXMLElement(dec *xml.Decoder, name string) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == name {
				depth++
			} else {
				// Different nested element; still increases depth.
				depth++
			}
		case xml.EndElement:
			depth--
			if depth == 0 {
				return nil
			}
		}
	}
	return nil
}
