package crawler

import (
	"io"
	"strings"

	"golang.org/x/net/html"
)

func extractHrefs(reader io.Reader) ([]string, error) {
	document, err := html.Parse(reader)
	if err != nil {
		return nil, err
	}

	var hrefs []string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && strings.EqualFold(node.Data, "a") {
			for _, attribute := range node.Attr {
				if strings.EqualFold(attribute.Key, "href") {
					hrefs = append(hrefs, attribute.Val)
					break
				}
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)

	return hrefs, nil
}
