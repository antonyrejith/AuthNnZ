package diff_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"

	"github.com/antonyrejith/portun/internal/modules/audit/internal/diff"
)

func norm(t *testing.T, s string) any {
	t.Helper()
	if s == "" {
		return nil
	}
	v, err := diff.Normalize(json.RawMessage(s))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestCompute checks entries, order and both values.
//
// Covers: GOV-11
func TestCompute(t *testing.T) {
	t.Parallel()
	n := func(s string) json.Number { return json.Number(s) }
	cases := []struct {
		name          string
		before, after string
		want          []diff.Entry
	}{
		{"equal", `{"a":1}`, `{"a":1}`, []diff.Entry{}},
		{"both missing", "", "", []diff.Entry{}},
		{"create", "", `{"b":2,"a":"x"}`, []diff.Entry{
			{Op: "add", Path: "/a", After: "x"},
			{Op: "add", Path: "/b", After: n("2")},
		}},
		{"delete", `{"a":true}`, "", []diff.Entry{{Op: "remove", Path: "/a", Before: true}}},
		{"replace nested", `{"p":{"x":1,"y":2}}`, `{"p":{"x":1,"y":3}}`, []diff.Entry{
			{Op: "replace", Path: "/p/y", Before: n("2"), After: n("3")},
		}},
		{"array whole", `{"l":[1,2]}`, `{"l":[1,2,3]}`, []diff.Entry{
			{Op: "replace", Path: "/l", Before: []any{n("1"), n("2")}, After: []any{n("1"), n("2"), n("3")}},
		}},
		{"type change", `{"a":{"b":1}}`, `{"a":"s"}`, []diff.Entry{
			{Op: "replace", Path: "/a", Before: map[string]any{"b": n("1")}, After: "s"},
		}},
		{"escaping", `{}`, `{"a/b~c":null}`, []diff.Entry{{Op: "add", Path: "/a~1b~0c"}}},
		{"root scalar", `1`, `2`, []diff.Entry{{Op: "replace", Path: "", Before: n("1"), After: n("2")}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := diff.Compute(norm(t, c.before), norm(t, c.after))
			if d := cmp.Diff(c.want, got); d != "" {
				t.Errorf("Compute (-want +got):\n%s", d)
			}
		})
	}
}

// TestNormalizeStruct checks that Go values diff by their JSON form.
//
// Covers: GOV-11
func TestNormalizeStruct(t *testing.T) {
	t.Parallel()
	type obj struct {
		Name string `json:"name"`
		N    int64  `json:"n"`
	}
	b, err := diff.Normalize(obj{Name: "a", N: 9007199254740993})
	if err != nil {
		t.Fatal(err)
	}
	a, err := diff.Normalize(obj{Name: "b", N: 9007199254740993})
	if err != nil {
		t.Fatal(err)
	}
	got := diff.Compute(b, a)
	want := []diff.Entry{{Op: "replace", Path: "/name", Before: "a", After: "b"}}
	if d := cmp.Diff(want, got); d != "" {
		t.Error(d)
	}
	if _, err := diff.Normalize(func() {}); err == nil {
		t.Error("Normalize accepted a function")
	}
}

func genValue(depth int) *rapid.Generator[any] {
	return rapid.Custom(func(t *rapid.T) any {
		kinds := 4
		if depth > 0 {
			kinds = 6
		}
		switch rapid.IntRange(0, kinds-1).Draw(t, "kind") {
		case 0:
			return nil
		case 1:
			return rapid.Bool().Draw(t, "bool")
		case 2:
			return json.Number(rapid.StringMatching(`-?[1-9][0-9]{0,5}`).Draw(t, "num"))
		case 3:
			return rapid.StringMatching(`[a-z/~]{0,4}`).Draw(t, "str")
		case 4:
			return genObject(depth-1).Draw(t, "obj")
		default:
			return rapid.SliceOfN(genValue(depth-1), 0, 3).Draw(t, "arr")
		}
	})
}

func genObject(depth int) *rapid.Generator[any] {
	return rapid.Custom(func(t *rapid.T) any {
		m := rapid.MapOfN(rapid.SampledFrom([]string{"a", "b", "c", "d/e", "f~g"}), genValue(depth), 0, 5).Draw(t, "map")
		return map[string]any(m)
	})
}

// TestApplyProperty: applying the diff of (before, after) to before yields
// after, and the entries are sorted by path.
//
// Covers: GOV-11
func TestApplyProperty(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		before := genObject(3).Draw(t, "before")
		after := genObject(3).Draw(t, "after")
		entries := diff.Compute(before, after)
		got, err := diff.Apply(before, entries)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if !reflect.DeepEqual(got, after) {
			t.Fatalf("Apply(before, diff) = %v, want %v", got, after)
		}
		for i := 1; i < len(entries); i++ {
			if entries[i-1].Path >= entries[i].Path {
				t.Fatalf("entries not sorted: %q then %q", entries[i-1].Path, entries[i].Path)
			}
		}
		if again := diff.Compute(before, after); !reflect.DeepEqual(again, entries) {
			t.Fatal("Compute is not deterministic")
		}
	})
}

// TestApplyRejectsWrongBase: a diff does not apply to another document.
//
// Covers: GOV-11
func TestApplyRejectsWrongBase(t *testing.T) {
	t.Parallel()
	entries := diff.Compute(norm(t, `{"a":1}`), norm(t, `{"a":2}`))
	if _, err := diff.Apply(norm(t, `{"a":5}`), entries); err == nil {
		t.Error("Apply accepted a document whose before value differs")
	}
	if _, err := diff.Apply(norm(t, `{"a":1}`), []diff.Entry{{Op: "move", Path: "/a"}}); err == nil {
		t.Error("Apply accepted an unknown op")
	}
	if _, err := diff.Split("a/b"); err == nil {
		t.Error("Split accepted a pointer without leading /")
	}
	if got, _ := diff.Split("/a~1b/c~0"); !reflect.DeepEqual(got, []string{"a/b", "c~"}) {
		t.Errorf("Split = %q", got)
	}
}
