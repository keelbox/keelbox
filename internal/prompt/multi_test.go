package prompt

import (
	"reflect"
	"strings"
	"testing"
)

func TestPickMany(t *testing.T) {
	for in, want := range map[string][]int{
		"":        nil,
		"a":       {0, 1, 2, 3},
		"ALL":     {0, 1, 2, 3},
		"1 3":     {0, 2},
		"3,1":     {0, 2},
		"2-4":     {1, 2, 3},
		"1 1 2-3": {0, 1, 2},
	} {
		got, ok := pickMany(in, 4)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v %v, want %v", in, got, ok, want)
		}
	}
	for _, in := range []string{"0", "5", "x", "3-2", "1-", "-2", "1 two"} {
		if _, ok := pickMany(in, 4); ok {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestMultiChoosePlain(t *testing.T) {
	choices := []Choice{{Label: "one", Detail: "first"}, {Label: "two"}, {Label: "three"}}
	var out strings.Builder
	got, err := New(strings.NewReader("9\n1 3\n"), &out, true).MultiChoose("Copy which?", choices)
	if err != nil || !reflect.DeepEqual(got, []int{0, 2}) {
		t.Fatalf("%v %v\n%s", got, err, out.String())
	}
	if !strings.Contains(out.String(), "type numbers from 1 to 3") || !strings.Contains(out.String(), "1) one    first") {
		t.Errorf("%s", out.String())
	}
	if _, err := New(strings.NewReader(""), &out, false).MultiChoose("q", choices); err != ErrNoTerminal {
		t.Errorf("no terminal: %v", err)
	}
}
