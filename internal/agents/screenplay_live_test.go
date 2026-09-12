package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestVerifyScreenplayLive runs the screenplay diff against a real
// production workspace (SCREENPLAY_WS=<workspace dir>) and prints the report.
// Skipped unless SCREENPLAY_WS is set.
func TestVerifyScreenplayLive(t *testing.T) {
	ws := os.Getenv("SCREENPLAY_WS")
	if ws == "" {
		t.Skip("set SCREENPLAY_WS=<workspace> to run against a real screenplay")
	}
	lines, err := loadScreenplayLines(filepath.Join(ws, "analysis", "screenplay.json"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(ws, "source", "source.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var script []string
	for _, l := range lines {
		script = append(script, words(l.Text)...)
	}
	diffs := wordDiff(words(string(src)), script)
	out, _ := json.MarshalIndent(diffs, "", "  ")
	t.Logf("%d shots, %d source words, %d screenplay words, %d differences:\n%s",
		len(lines), len(words(string(src))), len(script), len(diffs), out)
}

func TestWordDiffBasics(t *testing.T) {
	src := words("and he said where art thou and he said i heard thy voice")
	script := words("and he said where art thou i heard thy voice")
	d := wordDiff(src, script)
	if len(d) != 1 || d[0]["missing_from_screenplay"] != "and he said" {
		t.Fatalf("expected one missing run 'and he said', got %v", d)
	}
	if d := wordDiff(src, src); len(d) != 0 {
		t.Fatalf("identical texts must produce no differences, got %v", d)
	}
	extra := words("and he said where art thou verily and he said i heard thy voice")
	d = wordDiff(src, extra)
	if len(d) != 1 || d[0]["extra_in_screenplay"] != "verily" {
		t.Fatalf("expected one extra run 'verily', got %v", d)
	}
}
