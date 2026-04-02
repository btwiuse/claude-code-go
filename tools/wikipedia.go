package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/btwiuse/claude-code-go/types"
)

// WikipediaTool searches Wikipedia for articles and summaries.
type WikipediaTool struct{}

// WikipediaInput is the input schema for the Wikipedia tool.
type WikipediaInput struct {
	Query  string `json:"query"`
	Limit  int    `json:"limit,omitempty"`
	Lang   string `json:"lang,omitempty"`
}

// NewWikipediaTool creates a new WikipediaTool.
func NewWikipediaTool() *WikipediaTool {
	return &WikipediaTool{}
}

func (t *WikipediaTool) Name() string     { return "Wikipedia" }
func (t *WikipediaTool) IsReadOnly() bool { return true }
func (t *WikipediaTool) IsEnabled() bool  { return true }

func (t *WikipediaTool) Description() string {
	return `Search Wikipedia for articles and summaries on a given topic.

Use this tool to look up definitions, explanations, and background information about words, concepts, people, places, events, and more.
Returns article titles and snippets from Wikipedia search results.

Parameters:
- query: The search term or phrase to look up on Wikipedia.
- limit: Maximum number of results to return (1-10, default 3).
- lang: Wikipedia language edition to search (default "en" for English).`
}

func (t *WikipediaTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]types.ToolPropertySchema{
			"query": {
				Type:        "string",
				Description: "The search term or phrase to look up on Wikipedia.",
			},
			"limit": {
				Type:        "integer",
				Description: "Maximum number of results to return (1-10). Defaults to 3.",
				Default:     3,
			},
			"lang": {
				Type:        "string",
				Description: "Wikipedia language edition (e.g. \"en\", \"es\", \"fr\"). Defaults to \"en\".",
				Default:     "en",
			},
		},
		Required: []string{"query"},
	}
}

func (t *WikipediaTool) UserFacingName(input json.RawMessage) string {
	var in WikipediaInput
	if err := json.Unmarshal(input, &in); err == nil && in.Query != "" {
		q := in.Query
		if len(q) > 40 {
			q = q[:37] + "..."
		}
		return fmt.Sprintf("Wikipedia: %s", q)
	}
	return "Wikipedia"
}

// wikipediaSearchResponse represents the API response from Wikipedia search.
type wikipediaSearchResponse struct {
	Query struct {
		Search []struct {
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
			PageID  int    `json:"pageid"`
			WordCount int  `json:"wordcount"`
		} `json:"search"`
		SearchInfo struct {
			TotalHits int `json:"totalhits"`
		} `json:"searchinfo"`
	} `json:"query"`
}

func (t *WikipediaTool) Execute(ctx context.Context, input json.RawMessage, toolCtx *ToolContext) (*ToolResult, error) {
	var in WikipediaInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	if in.Query == "" {
		return &ToolResult{Content: "query is required", IsError: true}, nil
	}

	limit := 3
	if in.Limit > 0 {
		limit = in.Limit
	}
	if limit > 10 {
		limit = 10
	}

	lang := "en"
	if in.Lang != "" {
		lang = in.Lang
	}

	// Build Wikipedia API URL
	apiURL := fmt.Sprintf("https://%s.wikipedia.org/w/api.php", url.PathEscape(lang))
	params := url.Values{}
	params.Set("action", "query")
	params.Set("list", "search")
	params.Set("srsearch", in.Query)
	params.Set("srlimit", fmt.Sprintf("%d", limit))
	params.Set("format", "json")
	params.Set("utf8", "1")

	fullURL := apiURL + "?" + params.Encode()

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return &ToolResult{Content: fmt.Sprintf("Failed to create request: %v", err), IsError: true}, nil
	}
	req.Header.Set("User-Agent", "claude-code-go/0.1.0")

	resp, err := client.Do(req)
	if err != nil {
		return &ToolResult{Content: fmt.Sprintf("Wikipedia search failed: %v", err), IsError: true}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &ToolResult{
			Content: fmt.Sprintf("Wikipedia API returned HTTP %d: %s", resp.StatusCode, resp.Status),
			IsError: true,
		}, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return &ToolResult{Content: fmt.Sprintf("Error reading response: %v", err), IsError: true}, nil
	}

	var searchResp wikipediaSearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return &ToolResult{Content: fmt.Sprintf("Error parsing Wikipedia response: %v", err), IsError: true}, nil
	}

	results := searchResp.Query.Search
	if len(results) == 0 {
		return &ToolResult{Content: fmt.Sprintf("No Wikipedia results found for: %s", in.Query)}, nil
	}

	var out strings.Builder
	fmt.Fprintf(&out, "Wikipedia search results for \"%s\" (%d total hits):\n\n", in.Query, searchResp.Query.SearchInfo.TotalHits)

	for i, r := range results {
		snippet := stripHTML(r.Snippet)
		snippet = decodeHTMLEntities(snippet)

		fmt.Fprintf(&out, "%d. %s\n", i+1, r.Title)
		fmt.Fprintf(&out, "   %s\n", snippet)
		fmt.Fprintf(&out, "   URL: https://%s.wikipedia.org/wiki/%s\n", lang, url.PathEscape(strings.ReplaceAll(r.Title, " ", "_")))
		if i < len(results)-1 {
			fmt.Fprintln(&out)
		}
	}

	return &ToolResult{Content: out.String()}, nil
}
