package crawler

import (
	"io"
	"strings"

	"golang.org/x/net/html"
)

type extractedLinks struct {
	BaseHref *string
	Hrefs    []string
}

func extractLinks(reader io.Reader) (extractedLinks, error) {
	document, err := html.Parse(reader)
	if err != nil {
		return extractedLinks{}, err
	}

	var result extractedLinks
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch {
			case strings.EqualFold(node.Data, "base") && result.BaseHref == nil:
				for _, attribute := range node.Attr {
					if strings.EqualFold(attribute.Key, "href") {
						value := attribute.Val
						result.BaseHref = &value
						break
					}
				}
			case strings.EqualFold(node.Data, "a"):
				for _, attribute := range node.Attr {
					if strings.EqualFold(attribute.Key, "href") {
						result.Hrefs = append(result.Hrefs, attribute.Val)
						break
					}
				}
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)

	return result, nil
}
