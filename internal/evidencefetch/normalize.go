package evidencefetch

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

func normalize(body []byte, mediaType string) (string, error) {
	if !utf8.Valid(body) {
		return "", newError(CodeUnsupportedContent, "response body must be valid UTF-8", nil)
	}
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return normalizeHTML(body)
	}
	return strings.Join(strings.Fields(string(body)), " "), nil
}

func normalizeHTML(body []byte) (string, error) {
	document, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return "", newError(CodeUnsupportedContent, "HTML could not be parsed", err)
	}
	var parts []string
	var visit func(*html.Node, bool)
	visit = func(node *html.Node, hidden bool) {
		if node.Type == html.ElementNode {
			switch strings.ToLower(node.Data) {
			case "script", "style", "noscript", "svg", "template":
				hidden = true
			}
		}
		if node.Type == html.TextNode && !hidden {
			if text := strings.Join(strings.Fields(node.Data), " "); text != "" {
				parts = append(parts, text)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child, hidden)
		}
	}
	visit(document, false)
	return strings.Join(parts, " "), nil
}

func truncateUTF8(value string, limit int64) (string, bool, error) {
	if limit <= 0 || int64(len(value)) <= limit {
		return value, false, nil
	}
	if limit > int64(len(value)) {
		return "", false, fmt.Errorf("invalid truncation limit")
	}
	end := int(limit)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end], true, nil
}
