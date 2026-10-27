// Package webingest reads public web sources through the existing SSRF-safe node.
package webingest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/net/html"
	"workflow-optimizer/internal/node"
	nodehttp "workflow-optimizer/internal/node/http"
)

type Page struct {
	URL       string    `json:"url"`
	Text      string    `json:"text"`
	FetchedAt time.Time `json:"fetched_at"`
}

func Fetch(ctx context.Context, url string) (Page, error) {
	n := nodehttp.New()
	out, err := n.Execute(ctx, node.NodeInput{Config: map[string]any{"method": "GET", "url": url, "response_format": "text", "fail_on_error": true, "timeout_ms": 30000.0}})
	if err != nil {
		return Page{}, fmt.Errorf("the public page could not be read")
	}
	value, ok := out.GetPort("response")
	if !ok {
		return Page{}, fmt.Errorf("the page was empty")
	}
	text, ok := value.Data.(string)
	if !ok {
		return Page{}, fmt.Errorf("the page did not contain readable text")
	}
	text = Extract(text)
	if len(text) > 200000 {
		text = text[:200000]
	}
	if strings.TrimSpace(text) == "" {
		return Page{}, fmt.Errorf("the page had no readable text")
	}
	return Page{URL: url, Text: text, FetchedAt: time.Now().UTC()}, nil
}
func Extract(source string) string {
	if !strings.Contains(source, "<") {
		return strings.TrimSpace(source)
	}
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return strings.TrimSpace(source)
	}
	var b strings.Builder
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "nav", "footer", "svg":
				return
			}
			switch n.Data {
			case "p", "div", "tr", "li", "h1", "h2", "h3", "br":
				b.WriteString("\n")
			case "td", "th":
				b.WriteString("\t")
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(strings.Join(strings.Fields(n.Data), " "))
			b.WriteString(" ")
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
	lines := []string{}
	for _, line := range strings.Split(b.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
