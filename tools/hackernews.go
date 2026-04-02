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

// HackerNewsTool searches Hacker News stories and comments via the Algolia API.
type HackerNewsTool struct{}

// HackerNewsInput is the input schema for the HackerNews tool.
type HackerNewsInput struct {
	Query    string `json:"query"`
	Limit    int    `json:"limit,omitempty"`
	SortBy   string `json:"sort_by,omitempty"`
	Tags     string `json:"tags,omitempty"`
}

// NewHackerNewsTool creates a new HackerNewsTool.
func NewHackerNewsTool() *HackerNewsTool {
	return &HackerNewsTool{}
}

func (t *HackerNewsTool) Name() string     { return "HackerNews" }
func (t *HackerNewsTool) IsReadOnly() bool { return true }
func (t *HackerNewsTool) IsEnabled() bool  { return true }

func (t *HackerNewsTool) Description() string {
	return `Search Hacker News for stories, articles, and discussions using the Algolia API.

Use this tool to find tech-related articles, discussions, and community opinions about any topic. 
Great for discovering real-world usage examples, tutorials, opinions, and trending discussions about programming concepts, tools, and technologies.

Parameters:
- query: The search term or phrase.
- limit: Maximum number of results to return (1-20, default 5).
- sort_by: Sort order - "relevance" (default) or "date" for most recent first.
- tags: Filter by type - "story", "comment", "ask_hn", "show_hn", "front_page". Default is "story".`
}

func (t *HackerNewsTool) InputSchema() types.ToolInputSchema {
	return types.ToolInputSchema{
		Type: "object",
		Properties: map[string]types.ToolPropertySchema{
			"query": {
				Type:        "string",
				Description: "The search term or phrase to look up on Hacker News.",
			},
			"limit": {
				Type:        "integer",
				Description: "Maximum number of results to return (1-20). Defaults to 5.",
				Default:     5,
			},
			"sort_by": {
				Type:        "string",
				Description: "Sort order: \"relevance\" (default) or \"date\" for most recent first.",
				Enum:        []string{"relevance", "date"},
				Default:     "relevance",
			},
			"tags": {
				Type:        "string",
				Description: "Filter by type: \"story\", \"comment\", \"ask_hn\", \"show_hn\", \"front_page\". Defaults to \"story\".",
				Default:     "story",
			},
		},
		Required: []string{"query"},
	}
}

func (t *HackerNewsTool) UserFacingName(input json.RawMessage) string {
	var in HackerNewsInput
	if err := json.Unmarshal(input, &in); err == nil && in.Query != "" {
		q := in.Query
		if len(q) > 40 {
			q = q[:37] + "..."
		}
		return fmt.Sprintf("HackerNews: %s", q)
	}
	return "HackerNews"
}

// hackerNewsResponse represents the Algolia HN search API response.
type hackerNewsResponse struct {
	Hits []struct {
		Title       string   `json:"title"`
		URL         string   `json:"url"`
		Author      string   `json:"author"`
		Points      int      `json:"points"`
		NumComments int      `json:"num_comments"`
		CreatedAt   string   `json:"created_at"`
		ObjectID    string   `json:"objectID"`
		StoryText   string   `json:"story_text"`
		CommentText string   `json:"comment_text"`
		StoryTitle  string   `json:"story_title"`
		StoryURL    string   `json:"story_url"`
		Tags        []string `json:"_tags"`
	} `json:"hits"`
	NbHits int `json:"nbHits"`
}

func (t *HackerNewsTool) Execute(ctx context.Context, input json.RawMessage, toolCtx *ToolContext) (*ToolResult, error) {
	var in HackerNewsInput
	if err := json.Unmarshal(input, &in); err != nil {
		return &ToolResult{Content: fmt.Sprintf("Invalid input: %v", err), IsError: true}, nil
	}

	if in.Query == "" {
		return &ToolResult{Content: "query is required", IsError: true}, nil
	}

	limit := 5
	if in.Limit > 0 {
		limit = in.Limit
	}
	if limit > 20 {
		limit = 20
	}

	tags := "story"
	if in.Tags != "" {
		tags = in.Tags
	}

	// Build Algolia HN API URL
	var baseURL string
	if in.SortBy == "date" {
		baseURL = "https://hn.algolia.com/api/v1/search_by_date"
	} else {
		baseURL = "https://hn.algolia.com/api/v1/search"
	}

	params := url.Values{}
	params.Set("query", in.Query)
	params.Set("hitsPerPage", fmt.Sprintf("%d", limit))
	params.Set("tags", tags)

	fullURL := baseURL + "?" + params.Encode()

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
		return &ToolResult{Content: fmt.Sprintf("Hacker News search failed: %v", err), IsError: true}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &ToolResult{
			Content: fmt.Sprintf("Hacker News API returned HTTP %d: %s", resp.StatusCode, resp.Status),
			IsError: true,
		}, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return &ToolResult{Content: fmt.Sprintf("Error reading response: %v", err), IsError: true}, nil
	}

	var hnResp hackerNewsResponse
	if err := json.Unmarshal(body, &hnResp); err != nil {
		return &ToolResult{Content: fmt.Sprintf("Error parsing Hacker News response: %v", err), IsError: true}, nil
	}

	if len(hnResp.Hits) == 0 {
		return &ToolResult{Content: fmt.Sprintf("No Hacker News results found for: %s", in.Query)}, nil
	}

	var out strings.Builder
	fmt.Fprintf(&out, "Hacker News results for \"%s\" (%d total hits):\n\n", in.Query, hnResp.NbHits)

	for i, hit := range hnResp.Hits {
		isComment := hit.CommentText != ""

		if isComment {
			// Format comment result
			title := hit.StoryTitle
			if title == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(&out, "%d. Comment on: %s\n", i+1, title)
			fmt.Fprintf(&out, "   Author: %s | %s\n", hit.Author, hit.CreatedAt)

			commentText := stripHTML(hit.CommentText)
			commentText = decodeHTMLEntities(commentText)
			if len(commentText) > 300 {
				commentText = commentText[:297] + "..."
			}
			fmt.Fprintf(&out, "   %s\n", commentText)
		} else {
			// Format story result
			title := hit.Title
			if title == "" {
				title = "(untitled)"
			}
			fmt.Fprintf(&out, "%d. %s\n", i+1, title)
			fmt.Fprintf(&out, "   Author: %s | Points: %d | Comments: %d | %s\n",
				hit.Author, hit.Points, hit.NumComments, hit.CreatedAt)

			if hit.URL != "" {
				fmt.Fprintf(&out, "   URL: %s\n", hit.URL)
			}

			if hit.StoryText != "" {
				storyText := stripHTML(hit.StoryText)
				storyText = decodeHTMLEntities(storyText)
				if len(storyText) > 300 {
					storyText = storyText[:297] + "..."
				}
				fmt.Fprintf(&out, "   %s\n", storyText)
			}
		}

		fmt.Fprintf(&out, "   HN: https://news.ycombinator.com/item?id=%s\n", hit.ObjectID)
		if i < len(hnResp.Hits)-1 {
			fmt.Fprintln(&out)
		}
	}

	return &ToolResult{Content: out.String()}, nil
}
