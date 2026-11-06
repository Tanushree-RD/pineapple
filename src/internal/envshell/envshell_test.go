package envshell

import (
	"reflect"
	"testing"
)

func TestTokenizeQuotesLikeAShell(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`put students.db student:101 "name=Asha;city=Bengaluru;marks=88"`, []string{"put", "students.db", "student:101", "name=Asha;city=Bengaluru;marks=88"}},
		{`--iterations 20 --seed 999 --mode large-values`, []string{"--iterations", "20", "--seed", "999", "--mode", "large-values"}},
		{`put db k 'single quoted $x "kept"'`, []string{"put", "db", "k", `single quoted $x "kept"`}},
		{`put db k "say \"hi\" \\ done"`, []string{"put", "db", "k", `say "hi" \ done`}},
		{`a"b c"d  e`, []string{"ab cd", "e"}},
		{`put db k a\ b`, []string{"put", "db", "k", "a b"}},
		{`   `, nil},
		{``, nil},
		{`put db k ""`, []string{"put", "db", "k", ""}},
		{"put\tdb\tk\tv", []string{"put", "db", "k", "v"}},
		{`put db k "ünï cödé ✓"`, []string{"put", "db", "k", "ünï cödé ✓"}},
	}
	for _, c := range cases {
		got, err := Tokenize(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q -> %#v, want %#v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{`put "unclosed`, `put 'unclosed`} {
		if _, err := Tokenize(bad); err == nil {
			t.Errorf("%q: an unterminated quote must be an error", bad)
		}
	}
}

func TestStripPrefixLetsYouPasteAFullCommand(t *testing.T) {
	if got := StripPrefix([]string{"./bin/kvdb", "put", "a"}, "kvdb"); !reflect.DeepEqual(got, []string{"put", "a"}) {
		t.Fatal(got)
	}
	if got := StripPrefix([]string{"put", "a"}, "kvdb"); !reflect.DeepEqual(got, []string{"put", "a"}) {
		t.Fatal(got)
	}
	if got := StripPrefix([]string{"/hydra"}, "hydra"); len(got) != 1 {
		t.Fatalf("a slash command must not be taken for a program path: %v", got)
	}
	if got := StripPrefix(nil, "kvdb"); len(got) != 0 {
		t.Fatal(got)
	}
	if got := StripPrefix([]string{"/home/x/bin/torture", "--seed", "1"}, "torture", "hydra"); !reflect.DeepEqual(got, []string{"--seed", "1"}) {
		t.Fatal(got)
	}
}

func TestParseDBFlag(t *testing.T) {
	db, rest := ParseDBFlag([]string{"--db", "students.db", "x"})
	if db != "students.db" || !reflect.DeepEqual(rest, []string{"x"}) {
		t.Fatal(db, rest)
	}
	if db, rest := ParseDBFlag(nil); db != "" || len(rest) != 0 {
		t.Fatal(db, rest)
	}
}

func TestSwitchToReplacesTheProcessAndPassesTheDatabase(t *testing.T) {
	var gotPath string
	var gotArgv []string
	old := ExecFunc
	defer func() { ExecFunc = old }()
	ExecFunc = func(path string, argv []string, env []string) error {
		gotPath, gotArgv = path, argv
		return nil
	}
	// "go" is on every machine that runs these tests and stands in for the sibling program.
	if err := SwitchTo("go", "students.db"); err != nil {
		t.Fatal(err)
	}
	if gotPath == "" || !reflect.DeepEqual(gotArgv, []string{"go", "--db", "students.db"}) {
		t.Fatalf("path %q argv %v", gotPath, gotArgv)
	}
	if err := SwitchTo("definitely-not-a-real-program-xyz", ""); err == nil {
		t.Fatal("a missing sibling program must be an error that says what to do")
	}
}
