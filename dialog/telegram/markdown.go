package telegram

import (
	"strings"
)

// MarkdownToSimpleHTML converts common Markdown to a simplified HTML subset for Telegram.
func MarkdownToSimpleHTML(md string) string {
	var b strings.Builder
	b.Grow(len(md) + len(md)/4)

	lines := strings.Split(md, "\n")
	inCodeBlock := false
	codeLang := ""
	var codeLines []string

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "```") {
			if !inCodeBlock {
				inCodeBlock = true
				codeLang = strings.TrimPrefix(trimmed, "```")
				codeLines = nil
			} else {
				inCodeBlock = false
				if codeLang != "" {
					b.WriteString("<pre><code class=\"language-" + escapeHTML(codeLang) + "\">")
				} else {
					b.WriteString("<pre><code>")
				}
				b.WriteString(escapeHTML(strings.Join(codeLines, "\n")))
				b.WriteString("</code></pre>")
				if i < len(lines)-1 {
					b.WriteByte('\n')
				}
			}
			continue
		}

		if inCodeBlock {
			codeLines = append(codeLines, line)
			continue
		}

		if heading := reHeading.FindString(line); heading != "" {
			rest := strings.TrimPrefix(line, heading)
			b.WriteString("<b>")
			b.WriteString(convertInlineHTML(rest))
			b.WriteString("</b>")
		} else if strings.HasPrefix(trimmed, "> ") || trimmed == ">" {
			quote := strings.TrimPrefix(line, "> ")
			if quote == ">" {
				quote = ""
			}
			b.WriteString("<blockquote>")
			b.WriteString(convertInlineHTML(quote))
			b.WriteString("</blockquote>")
		} else if reHorizontal.MatchString(trimmed) {
			b.WriteString("———")
		} else {
			b.WriteString(convertInlineHTML(line))
		}

		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}

	if inCodeBlock && len(codeLines) > 0 {
		b.WriteString("<pre><code>")
		b.WriteString(escapeHTML(strings.Join(codeLines, "\n")))
		b.WriteString("</code></pre>")
	}

	return b.String()
}

func convertInlineHTML(s string) string {
	type placeholder struct {
		key  string
		html string
	}
	var phs []placeholder
	phIdx := 0

	nextPH := func(html string) string {
		key := "\x00PH" + string(rune('0'+phIdx)) + "\x00"
		phs = append(phs, placeholder{key: key, html: html})
		phIdx++
		return key
	}

	s = reInlineCodeHTML.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[1 : len(m)-1]
		return nextPH("<code>" + escapeHTML(inner) + "</code>")
	})

	s = reLinkHTML.ReplaceAllStringFunc(s, func(m string) string {
		sm := reLinkHTML.FindStringSubmatch(m)
		if len(sm) < 3 {
			return m
		}
		return nextPH(`<a href="` + escapeHTML(sm[2]) + `">` + escapeHTML(sm[1]) + `</a>`)
	})

	s = escapeHTML(s)

	s = reBoldAstHTML.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[2 : len(m)-2]
		return nextPH("<b>" + inner + "</b>")
	})
	s = reBoldUndHTML.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[2 : len(m)-2]
		return nextPH("<b>" + inner + "</b>")
	})

	s = reStrikeHTML.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[2 : len(m)-2]
		return nextPH("<s>" + inner + "</s>")
	})

	s = reItalicAstHTML.ReplaceAllStringFunc(s, func(m string) string {
		idx := strings.Index(m, "*")
		if idx < 0 {
			return m
		}
		lastIdx := strings.LastIndex(m, "*")
		if lastIdx <= idx {
			return m
		}
		return m[:idx] + "<i>" + m[idx+1:lastIdx] + "</i>" + m[lastIdx+1:]
	})

	for i := 0; i < 3; i++ {
		changed := false
		for _, ph := range phs {
			if strings.Contains(s, ph.key) {
				s = strings.Replace(s, ph.key, ph.html, 1)
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	return s
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
