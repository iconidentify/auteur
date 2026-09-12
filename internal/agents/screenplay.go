package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"auteur/pkg/harness"
)

// RegisterScreenplayTools adds verify_screenplay: a deterministic check of a
// film screenplay against its source text. The rules it enforces are the
// ones the crew otherwise has to keep by counting — every source word spoken
// in order, no line over the clip cap, no orphaned attribution tags — and a
// model counting words is exactly how those rules got broken.
func (t *Toolbox) RegisterScreenplayTools(reg *harness.Registry) {
	reg.Register(harness.ToolDefinition{
		Name: "verify_screenplay",
		Description: "Check analysis/screenplay.json against source/source.txt: reports every word of " +
			"the source that is missing, changed, or out of order (with context), every line over " +
			"35 words, every stand-alone narrator tag under 6 words that could fold into a " +
			"neighbouring narrator line, and the speaker tally. Run it before finishing a " +
			"screenplay and before accepting one; fix everything it lists.",
		InputSchema: harness.Obj(map[string]any{
			"screenplay_path": harness.Str("Screenplay JSON (default analysis/screenplay.json)"),
			"source_path":     harness.Str("Source text (default source/source.txt)"),
		}),
	}, func(ctx context.Context, input map[string]any) (string, error) {
		sp := firstNonEmpty(str(input, "screenplay_path"), "analysis/screenplay.json")
		src := firstNonEmpty(str(input, "source_path"), "source/source.txt")
		lines, err := loadScreenplayLines(t.resolve(sp))
		if err != nil {
			return "", err
		}
		srcBytes, err := os.ReadFile(t.resolve(src))
		if err != nil {
			return "", fmt.Errorf("read source: %w", err)
		}

		var scriptWords []string
		for _, l := range lines {
			scriptWords = append(scriptWords, words(l.Text)...)
		}
		srcWords := words(string(srcBytes))
		diffs := wordDiff(srcWords, scriptWords)

		var long []map[string]any
		var tags []map[string]any
		tally := map[string]int{}
		for i, l := range lines {
			n := len(strings.Fields(l.Text))
			tally[l.Speaker]++
			if n > 35 {
				long = append(long, map[string]any{"id": l.ID, "words": n})
			}
			if strings.EqualFold(l.Speaker, "narrator") && n < 6 {
				foldable := ""
				if i > 0 && strings.EqualFold(lines[i-1].Speaker, "narrator") {
					foldable = "fold onto the end of " + lines[i-1].ID
				} else if i+1 < len(lines) && strings.EqualFold(lines[i+1].Speaker, "narrator") {
					foldable = "fold onto the start of " + lines[i+1].ID
				} else {
					foldable = "may stand alone: both neighbours are dialogue"
				}
				tags = append(tags, map[string]any{"id": l.ID, "text": l.Text, "advice": foldable})
			}
		}

		ok := len(diffs) == 0 && len(long) == 0
		verdict := "PASS: every source word is spoken in order and no line exceeds 35 words."
		if !ok {
			verdict = "FAIL: fix the items below, then run verify_screenplay again."
		}
		return jsonOut(map[string]any{
			"ok":                  ok,
			"verdict":             verdict,
			"source_words":        len(srcWords),
			"screenplay_words":    len(scriptWords),
			"shots":               len(lines),
			"speakers":            tally,
			"text_differences":    diffs,
			"lines_over_35":       long,
			"short_narrator_tags": tags,
		})
	})
}

type screenplayLine struct {
	ID, Speaker, Text string
}

// loadScreenplayLines accepts both shapes the screenwriter has produced: a
// shot with `speaker` and a string `line`, or a shot whose `line` is an
// object {speaker, text}.
func loadScreenplayLines(path string) ([]screenplayLine, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read screenplay: %w", err)
	}
	var doc struct {
		Scenes []struct {
			Shots []map[string]any `json:"shots"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	var out []screenplayLine
	for _, sc := range doc.Scenes {
		for _, sh := range sc.Shots {
			l := screenplayLine{ID: fmt.Sprint(sh["id"])}
			switch v := sh["line"].(type) {
			case string:
				l.Text = v
				l.Speaker = fmt.Sprint(sh["speaker"])
			case map[string]any:
				l.Text = fmt.Sprint(v["text"])
				l.Speaker = fmt.Sprint(v["speaker"])
			}
			if l.Speaker == "<nil>" || l.Speaker == "" {
				l.Speaker = "narrator"
			}
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no shots found in %s", filepath.Base(path))
	}
	return out, nil
}

var wordRe = regexp.MustCompile(`[a-z0-9']+`)

func words(s string) []string {
	return wordRe.FindAllString(strings.ToLower(s), -1)
}

// wordDiff reports where the script departs from the source, as the source
// run that is missing or the script run that is extra, each with a little
// context so the crew can find the spot. Plain LCS: the texts are short.
func wordDiff(src, script []string) []map[string]any {
	n, m := len(src), len(script)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if src[i] == script[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []map[string]any
	ctx := func(ws []string, from, to int) string {
		a, b := from-4, to+4
		if a < 0 {
			a = 0
		}
		if b > len(ws) {
			b = len(ws)
		}
		return strings.Join(ws[a:b], " ")
	}
	i, j := 0, 0
	for i < n || j < m {
		if i < n && j < m && src[i] == script[j] {
			i++
			j++
			continue
		}
		si, sj := i, j
		for i < n && (j >= m || lcs[i+1][j] >= lcs[i][j+1]) && !(j < m && src[i] == script[j]) {
			i++
			if j < m && i < n && src[i] == script[j] {
				break
			}
		}
		for j < m && !(i < n && src[i] == script[j]) {
			j++
		}
		d := map[string]any{}
		if i > si {
			d["missing_from_screenplay"] = strings.Join(src[si:i], " ")
			d["source_context"] = ctx(src, si, i)
		}
		if j > sj {
			d["extra_in_screenplay"] = strings.Join(script[sj:j], " ")
			d["screenplay_context"] = ctx(script, sj, j)
		}
		if len(d) > 0 {
			out = append(out, d)
		}
		if i == si && j == sj { // safety: always make progress
			i++
			j++
		}
	}
	return out
}
