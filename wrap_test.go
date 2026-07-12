package main

import (
	"strings"
	"testing"
)

func longestLine(s string) int {
	longest := 0
	for _, line := range strings.Split(s, "\n") {
		if n := len([]rune(line)); n > longest {
			longest = n
		}
	}
	return longest
}

func TestWrapBodyWrapsProseAt80(t *testing.T) {
	body := strings.Repeat("badger ", 40)
	got := wrapBody(body, 80)
	if longestLine(got) > 80 {
		t.Fatalf("line exceeded 80 columns: %d", longestLine(got))
	}
	if !strings.Contains(got, "\n") {
		t.Fatal("expected the long line to be wrapped")
	}
}

func TestWrapBodyKeepsQuotePrefix(t *testing.T) {
	body := "> " + strings.Repeat("quoted words here ", 12)
	got := wrapBody(body, 80)
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "> ") {
			t.Fatalf("continuation line lost its quote prefix: %q", line)
		}
	}
	if longestLine(got) > 80 {
		t.Fatalf("quoted line exceeded 80: %d", longestLine(got))
	}
}

func TestWrapBodyLeavesCodeFencesAlone(t *testing.T) {
	long := strings.Repeat("x", 120)
	body := "```\n" + long + "\n```"
	got := wrapBody(body, 80)
	if got != body {
		t.Fatalf("code fence was modified:\n%q", got)
	}
}

func TestWrapBodyNeverBreaksLongURLs(t *testing.T) {
	url := "https://example.com/" + strings.Repeat("a", 100)
	got := wrapBody("see "+url, 80)
	if !strings.Contains(got, url) {
		t.Fatal("long URL was broken across lines")
	}
}

func TestWrapBodyDisabledWhenZero(t *testing.T) {
	body := strings.Repeat("badger ", 40)
	if got := wrapBody(body, 0); got != body {
		t.Fatal("wrapping should be off when cols <= 0")
	}
}

func TestVimGetsTextwidthArgs(t *testing.T) {
	args := editorWrapArgs("vim", 80)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "textwidth=80") || !strings.Contains(joined, "formatoptions+=t") {
		t.Fatalf("vim did not get wrap args: %v", args)
	}
	if editorWrapArgs("vim", 0) != nil {
		t.Fatal("no args expected when wrapping is off")
	}
}

func TestCompactAPIErrorSummarisesFirewallHTML(t *testing.T) {
	html := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Request Blocked - RootBadger Firewall</title>
    <style>body { background-color: #f8f9fa; }</style>
</head>
<body><div class="container">blocked</div></body></html>`

	got := compactAPIError([]byte(html))
	if got != "Request Blocked - RootBadger Firewall" {
		t.Fatalf("expected the page title, got %q", got)
	}
	if strings.Contains(got, "<") {
		t.Fatal("raw markup leaked into the error message")
	}
}

func TestCompactAPIErrorStillPrefersJSONMessage(t *testing.T) {
	got := compactAPIError([]byte(`{"message":"You are not subscribed to this group."}`))
	if got != "You are not subscribed to this group." {
		t.Fatalf("JSON message regressed: %q", got)
	}
}

func TestRequestLabelIsFriendly(t *testing.T) {
	cases := map[[2]string]string{
		{"GET", "/api/v1/app/subscriptions"}:            "Fetching subscriptions…",
		{"DELETE", "/api/v1/app/groups/rb.x/subscribe"}: "Unsubscribing on rootbadger…",
		{"POST", "/api/v1/app/groups/rb.x/subscribe"}:   "Subscribing on rootbadger…",
		{"GET", "/api/v1/app/threads/123"}:              "Fetching thread…",
		{"PUT", "/api/v1/app/profile"}:                  "Saving profile…",
	}
	for k, want := range cases {
		if got := requestLabel(k[0], k[1]); got != want {
			t.Errorf("requestLabel(%q,%q) = %q, want %q", k[0], k[1], got, want)
		}
	}
	if requestLabel("GET", "/api/v1/unknown") != "Loading…" {
		t.Error("unknown GET should fall back to Loading")
	}
}

func TestFormatTimestamp(t *testing.T) {
	if got := formatTimestamp("2020-01-02T15:04:05Z"); got != "2020-01-02 15:04" && !strings.HasSuffix(got, ":04") {
		// Local timezone shifts the clock; the date form is what matters.
		if len(got) != len("2006-01-02 15:04") {
			t.Fatalf("old timestamp should render absolute, got %q", got)
		}
	}
	if got := formatTimestamp(""); got != "" {
		t.Fatalf("empty in, empty out; got %q", got)
	}
	if got := formatTimestamp("not-a-date"); got != "not-a-date" {
		t.Fatalf("unparseable input should pass through, got %q", got)
	}
}
