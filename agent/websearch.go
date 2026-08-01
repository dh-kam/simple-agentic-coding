package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

func NewWebSearchTool() Tool {
	return Tool{
		Name:        "web_search",
		Description: "웹에서 검색어를 검색하고 상위 결과의 제목과 URL을 반환한다.",
		InputSchema: map[string]any{
			"query": map[string]any{"type": "string", "description": "검색어"},
		},
		Run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Query string `json:"query"`
			}
			json.Unmarshal(args, &in)
			if in.Query == "" {
				return "", fmt.Errorf("query required")
			}
			return duckDuckGoSearch(ctx, in.Query)
		},
	}
}

func duckDuckGoSearch(ctx context.Context, query string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, _ := http.NewRequestWithContext(cctx, "GET", u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// Parse result links from HTML
	resultRe := regexp.MustCompile(`class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	matches := resultRe.FindAllStringSubmatch(string(body), -1)
	if len(matches) == 0 {
		return "no results", nil
	}
	var sb strings.Builder
	for i, m := range matches {
		if i >= 10 {
			break
		}
		link := m[1]
		title := strings.TrimSpace(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(m[2], ""))
		if strings.HasPrefix(link, "//") {
			link = "https:" + link
		}
		sb.WriteString(fmt.Sprintf("%d. %s\n   %s\n", i+1, title, link))
	}
	return sb.String(), nil
}
