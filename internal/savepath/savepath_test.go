package savepath

import (
	"strings"
	"testing"
)

func TestParse_EmptyYieldsEmptyResolver(t *testing.T) {
	r, err := Parse("")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(r.Names()) != 0 {
		t.Errorf("names = %v, want empty", r.Names())
	}
}

func TestParse_SingleAlias(t *testing.T) {
	r, err := Parse("kura-inbox=/mnt/kura")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	path, err := r.Resolve("kura-inbox")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if path != "/mnt/kura" {
		t.Errorf("path = %q, want /mnt/kura", path)
	}
}

func TestParse_MultipleAliasesSortedNames(t *testing.T) {
	r, err := Parse("downloads=/mnt/downloads,kura-inbox=/mnt/kura,archive=/mnt/archive")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"archive", "downloads", "kura-inbox"}
	got := r.Names()
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParse_WhitespaceTolerated(t *testing.T) {
	r, err := Parse("  kura-inbox = /mnt/kura ,  downloads=/mnt/downloads  ")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	path, _ := r.Resolve("kura-inbox")
	if path != "/mnt/kura" {
		t.Errorf("path = %q, want /mnt/kura (trimmed)", path)
	}
}

func TestParse_RejectsMalformedEntry(t *testing.T) {
	cases := []string{
		"no-equals-sign",
		"=missing-name",
		"=", // both empty
	}
	for _, in := range cases {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", in)
		}
	}
}

func TestParse_RejectsDuplicateName(t *testing.T) {
	if _, err := Parse("a=/x,a=/y"); err == nil {
		t.Error("expected duplicate-name error")
	}
}

func TestNew_RejectsBadName(t *testing.T) {
	cases := []map[string]string{
		{"Has-Caps": "/x"},
		{"has_underscore": "/x"},
		{"-leading-hyphen": "/x"},
		{"has space": "/x"},
	}
	for _, in := range cases {
		if _, err := New(in); err == nil {
			t.Errorf("New(%v) succeeded, want error", in)
		}
	}
}

func TestNew_RejectsEmptyPath(t *testing.T) {
	if _, err := New(map[string]string{"valid-name": ""}); err == nil {
		t.Error("expected empty-path error")
	}
}

func TestResolve_EmptyAllowed(t *testing.T) {
	r, _ := Parse("anything=/x")
	path, err := r.Resolve("")
	if err != nil {
		t.Errorf("Resolve(\"\"): %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty (signals \"leave save_path unset\")", path)
	}
}

func TestResolve_UnknownNameErrors(t *testing.T) {
	r, _ := Parse("known=/x")
	_, err := r.Resolve("unknown")
	if err == nil {
		t.Fatal("expected error for unknown name")
	}
	if !strings.Contains(err.Error(), "known") {
		t.Errorf("error %q should list configured names", err)
	}
}

func TestResolve_UnknownOnEmptyResolverHintsNoConfig(t *testing.T) {
	r, _ := Parse("")
	_, err := r.Resolve("anything")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "no destinations configured") {
		t.Errorf("error %q should hint no destinations are configured", err)
	}
}

func TestDescriptionHint_Empty(t *testing.T) {
	r, _ := Parse("")
	got := r.DescriptionHint()
	if !strings.Contains(got, "No destinations") {
		t.Errorf("hint = %q, should explain empty case", got)
	}
}

func TestNameForPath_MatchesAlias(t *testing.T) {
	r, _ := Parse("kura-inbox=/mnt/kura,downloads=/mnt/downloads")
	if got := r.NameForPath("/mnt/kura"); got != "kura-inbox" {
		t.Errorf("NameForPath(/mnt/kura) = %q, want kura-inbox", got)
	}
}

func TestNameForPath_NoMatchReturnsEmpty(t *testing.T) {
	r, _ := Parse("kura-inbox=/mnt/kura")
	if got := r.NameForPath("/some/other/path"); got != "" {
		t.Errorf("NameForPath unmatched = %q, want empty", got)
	}
}

func TestNameForPath_EmptyInputReturnsEmpty(t *testing.T) {
	r, _ := Parse("kura-inbox=/mnt/kura")
	if got := r.NameForPath(""); got != "" {
		t.Errorf("NameForPath(\"\") = %q, want empty", got)
	}
}

func TestDescriptionHint_PopulatedListsNames(t *testing.T) {
	r, _ := Parse("downloads=/mnt/downloads,kura-inbox=/mnt/kura")
	got := r.DescriptionHint()
	for _, name := range []string{"downloads", "kura-inbox"} {
		if !strings.Contains(got, name) {
			t.Errorf("hint %q missing %q", got, name)
		}
	}
}
