package imap

import (
	"html"
	"regexp"
	"strings"
)

var (
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</\s*(script|style|head)\s*>`)
	reComment     = regexp.MustCompile(`(?s)<!--.*?-->`)
	reBreak       = regexp.MustCompile(`(?i)<\s*br\s*/?\s*>`)
	reBlockEnd    = regexp.MustCompile(`(?i)</\s*(p|div|tr|li|h[1-6]|table|ul|ol|blockquote)\s*>`)
	reListItem    = regexp.MustCompile(`(?i)<\s*li[^>]*>`)
	reCell        = regexp.MustCompile(`(?i)</\s*t[dh]\s*>`)
	reTag         = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces      = regexp.MustCompile(`[ \t\x{00a0}]+`)
	reBlankLines  = regexp.MustCompile(`\n{3,}`)
)

// HTMLToText converts an HTML fragment to readable plain text. It is the
// exported entry point used by other packages (e.g. signature rendering);
// the behaviour mirrors the internal htmlToText used when rendering emails.
func HTMLToText(s string) string {
	return htmlToText(s)
}

// htmlToText converts an HTML body to readable plain text while keeping the
// line structure: block-level tags become newlines, links keep their URL.
func htmlToText(s string) string {
	s = reScriptStyle.ReplaceAllString(s, "")
	s = reComment.ReplaceAllString(s, "")
	s = extractLinks(s)
	s = reBreak.ReplaceAllString(s, "\n")
	s = reListItem.ReplaceAllString(s, "\n- ")
	s = reCell.ReplaceAllString(s, "\t")
	s = reBlockEnd.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)

	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
	}
	s = strings.Join(lines, "\n")
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

var (
	reAnchor  = regexp.MustCompile(`(?is)<a\s[^>]*href\s*=\s*["']([^"']+)["'][^>]*>(.*?)</\s*a\s*>`)
	reHref    = regexp.MustCompile(`(?is)href\s*=\s*["']([^"']+)["']`)
	reTracker = regexp.MustCompile(`(?i)(unsubscribe|desabonn|desinscri|optout|opt-out|/track|/open|/pixel|utm_|\.gif|\.png|\.jpg)`)
	// Known link-wrapping trackers whose URLs carry no useful information.
	reTrackerHost = regexp.MustCompile(`(?i)^https?://[^/]*(mailtrack\.com|list-manage\.com|sendgrid\.net|\.mailjet\.com/r/|awstrack\.me|clicks\.|links?\.|email\.[^/]*/c/)`)
	reHost        = regexp.MustCompile(`(?i)^https?://([^/?#]+)`)
)

// maxExtractedLinks caps the link list so marketing footers do not drown the
// actionable ones; maxPerHost keeps a single domain from filling the quota.
const (
	maxExtractedLinks = 20
	maxPerHost        = 3
)

// ExtractLinks returns the actionable links of an HTML body: mailto: addresses
// first (reply relays such as messagerie.leboncoin.fr), then regular URLs,
// deduplicated and stripped of obvious tracking/unsubscribe noise.
func ExtractLinks(htmlBody string) []string {
	if htmlBody == "" {
		return nil
	}
	seen := map[string]bool{}
	perHost := map[string]int{}
	var mailtos, urls []string

	for _, m := range reHref.FindAllStringSubmatch(htmlBody, -1) {
		href := strings.TrimSpace(html.UnescapeString(m[1]))
		if href == "" || strings.HasPrefix(href, "#") || seen[href] {
			continue
		}
		seen[href] = true

		switch {
		case strings.HasPrefix(strings.ToLower(href), "mailto:"):
			mailtos = append(mailtos, href)
		case strings.HasPrefix(strings.ToLower(href), "tel:"):
			mailtos = append(mailtos, href)
		case strings.HasPrefix(strings.ToLower(href), "http"):
			if reTracker.MatchString(href) || reTrackerHost.MatchString(href) {
				continue
			}
			host := ""
			if m := reHost.FindStringSubmatch(href); m != nil {
				host = strings.ToLower(m[1])
			}
			if perHost[host] >= maxPerHost {
				continue
			}
			perHost[host]++
			urls = append(urls, href)
		}
	}

	out := append(mailtos, urls...)
	if len(out) > maxExtractedLinks {
		out = out[:maxExtractedLinks]
	}
	return out
}

// extractLinks rewrites <a href="URL">label</a> as "label <URL>" so that the
// actionable URLs (reply links, mailto: relays, listing URLs) survive the
// tag stripping.
func extractLinks(s string) string {
	return reAnchor.ReplaceAllStringFunc(s, func(m string) string {
		sub := reAnchor.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		href := strings.TrimSpace(sub[1])
		label := strings.TrimSpace(reTag.ReplaceAllString(sub[2], ""))
		label = strings.TrimSpace(html.UnescapeString(label))
		// Parentheses, not angle brackets: the tag-stripping pass that runs
		// after this one would eat anything shaped like <...>.
		switch {
		case href == "" || strings.HasPrefix(href, "#"):
			return label
		case label == "" || label == href:
			return " " + href + " "
		default:
			return " " + label + " (" + href + ") "
		}
	})
}
