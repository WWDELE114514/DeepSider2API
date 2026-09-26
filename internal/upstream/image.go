package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ImageResult is the outcome of an image generation call.
type ImageResult struct {
	URLs          []string
	DeductCredits int
	Failed        bool
}

var (
	mdImageRe = regexp.MustCompile(`!\[[^\]]*\]\(\s*(https?://[^)\s]+)\s*\)`)
	mdLinkRe  = regexp.MustCompile(`\[[^\]]*\]\(\s*(https?://[^)\s]+)\s*\)`)
	bareURLRe = regexp.MustCompile(`https?://[^\s)\]"'<>]+`)
)

func looksLikeImage(u string) bool {
	lu := strings.ToLower(u)
	if strings.Contains(lu, "cdnforfiles") || strings.Contains(lu, "static.deepsider") {
		return true
	}
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif"} {
		if strings.Contains(lu, ext) {
			return true
		}
	}
	return false
}

// ExtractImageURLs pulls image URLs out of a 202 content frame, which is
// markdown like "![image](URL)\n[download](URL)".
func ExtractImageURLs(s string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(u string) {
		u = strings.TrimRight(u, ".,);]")
		if u == "" || seen[u] || !looksLikeImage(u) {
			return
		}
		seen[u] = true
		out = append(out, u)
	}
	for _, m := range mdImageRe.FindAllStringSubmatch(s, -1) {
		add(m[1])
	}
	for _, m := range mdLinkRe.FindAllStringSubmatch(s, -1) {
		add(m[1])
	}
	for _, m := range bareURLRe.FindAllString(s, -1) {
		add(m)
	}
	return out
}

// GenerateImage runs an image generation through the conversation endpoint and
// extracts the resulting image URLs from the SSE stream.
func (c *Client) GenerateImage(ctx context.Context, token string, body map[string]interface{}) (ImageResult, error) {
	var urls []string
	seen := map[string]bool{}
	var deduct int

	err := c.Conversation(ctx, token, body, func(ev Event) error {
		switch ev.Code {
		case 202:
			var d struct {
				Type      string `json:"type"`
				Content   string `json:"content"`
				Heartbeat bool   `json:"heartbeat"`
			}
			if json.Unmarshal(ev.Data, &d) != nil {
				return nil
			}
			if d.Heartbeat || strings.TrimSpace(d.Content) == "" {
				return nil
			}
			for _, u := range ExtractImageURLs(d.Content) {
				if !seen[u] {
					seen[u] = true
					urls = append(urls, u)
				}
			}
		case 203:
			var d struct {
				DeductCredits int  `json:"deductCredits"`
				NeedDeduct    bool `json:"needDeduct"`
			}
			if json.Unmarshal(ev.Data, &d) == nil {
				deduct = d.DeductCredits
			}
		case 1002, 1003, 2002, 3004:
			return fmt.Errorf("deepsider code %d: %s", ev.Code, ev.Message)
		}
		return nil
	})
	if err != nil {
		return ImageResult{}, err
	}
	return ImageResult{URLs: urls, DeductCredits: deduct, Failed: len(urls) == 0}, nil
}
