package main

import (
	"bytes"
	"fmt"
	"net/url"

	"github.com/charmbracelet/glamour/ansi"
	"github.com/muesli/termenv"
	"github.com/yuin/goldmark/ast"
	astext "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// expandTableReferences keeps compact numbered labels in cells, but moves the
// actual links into paragraphs, whose destinations glamour does not truncate.
// Work on parsed nodes so reference-style links, fences and escaped Markdown
// keep their normal meaning. The source used for copying is never modified.
func expandTableReferences(doc ast.Node, source []byte, style ansi.StyleConfig) ([]byte, error) {
	addText := func(s string) *ast.Text {
		start := len(source)
		source = append(source, s...)
		return ast.NewTextSegment(text.NewSegment(start, len(source)))
	}
	ctx := ansi.NewRenderContext(ansi.Options{Styles: style, ColorProfile: termenv.TrueColor})
	err := ast.Walk(doc, func(table ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || table.Kind() != astext.KindTable {
			return ast.WalkContinue, nil
		}
		var nodes []ast.Node
		_ = ast.Walk(table, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
			if entering {
				switch n.(type) {
				case *ast.Image, *ast.Link, *ast.AutoLink:
					nodes = append(nodes, n)
					return ast.WalkSkipChildren, nil
				}
			}
			return ast.WalkContinue, nil
		})
		// Glamour numbers links and images separately, deduplicating each list.
		var refs [2][]ast.Node
		type key struct{ href, title, label string }
		seen := [2]map[key]int{{}, {}}
		for _, n := range nodes {
			k := key{label: string(n.Text(source))}
			group := 0
			switch link := n.(type) {
			case *ast.Image:
				group = 1
				k.href, k.title = string(link.Destination), string(link.Title)
			case *ast.Link:
				k.href, k.title = string(link.Destination), string(link.Title)
			case *ast.AutoLink:
				k.href = string(link.URL(source))
				k.label = ""
			}
			if k.label == "" {
				if u, err := url.Parse(k.href); err == nil {
					k.label = u.Hostname()
				}
			}
			number, exists := seen[group][k]
			if !exists {
				number = len(refs[group]) + 1
				seen[group][k] = number
				refs[group] = append(refs[group], n)
			}
			label := fmt.Sprintf("%s[%d]", k.label, number)
			var cell bytes.Buffer
			var element ansi.ElementRenderer = &ansi.BaseElement{Token: label, Style: style.LinkText}
			if group == 1 {
				element = &ansi.ImageElement{Text: label, TextOnly: true}
			}
			if err := element.Render(&cell, ctx); err != nil {
				return ast.WalkStop, err
			}
			n.Parent().ReplaceChild(n.Parent(), n, addText(cell.String()))
		}
		after := table
		for _, group := range refs {
			for i, n := range group {
				p := ast.NewParagraph()
				p.AppendChild(p, addText(fmt.Sprintf("[%d]: ", i+1)))
				p.AppendChild(p, n)
				table.Parent().InsertAfter(table.Parent(), after, p)
				after = p
			}
		}
		return ast.WalkSkipChildren, nil
	})
	return source, err
}
