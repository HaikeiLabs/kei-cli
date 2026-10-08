package harness

import (
	"strings"
	"testing"
)

func TestOrderedJSONPreservesKeyOrder(t *testing.T) {
	input := `{"zeta":1,"alpha":{"y":2,"b":3},"mid":[1,2],"model":"claude-sonnet"}`
	v, err := parseOrdered([]byte(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := marshalOrdered(v, "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	// Top-level order must be zeta, alpha, mid, model (not alphabetical).
	zetaIdx := strings.Index(got, `"zeta"`)
	alphaIdx := strings.Index(got, `"alpha"`)
	midIdx := strings.Index(got, `"mid"`)
	modelIdx := strings.Index(got, `"model"`)
	if !(zetaIdx < alphaIdx && alphaIdx < midIdx && midIdx < modelIdx) {
		t.Fatalf("top-level key order not preserved:\n%s", got)
	}
	// Nested order must be y, b.
	yIdx := strings.Index(got, `"y"`)
	bIdx := strings.Index(got, `"b"`)
	if !(yIdx < bIdx) {
		t.Fatalf("nested key order not preserved:\n%s", got)
	}
}

func TestOrderedJSONDoesNotEscapeHTML(t *testing.T) {
	input := `{"url":"https://a<b>&c"}`
	v, err := parseOrdered([]byte(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := marshalOrdered(v, "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), `\u003c`) || strings.Contains(string(out), `\u003e`) || strings.Contains(string(out), `\u0026`) {
		t.Fatalf("HTML was escaped: %s", out)
	}
	if !strings.Contains(string(out), `https://a<b>&c`) {
		t.Fatalf("value mangled: %s", out)
	}
}

func TestOrderedJSONPreservesNumberLiteral(t *testing.T) {
	input := `{"n":1.0,"i":2}`
	v, err := parseOrdered([]byte(input))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := marshalOrdered(v, "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"n": 1.0`) || !strings.Contains(string(out), `"i": 2`) {
		t.Fatalf("number literals not preserved: %s", out)
	}
}

func TestOrderedJSONEmptyContainers(t *testing.T) {
	for _, input := range []string{`{}`, `[]`} {
		v, err := parseOrdered([]byte(input))
		if err != nil {
			t.Fatalf("parse %s: %v", input, err)
		}
		out, err := marshalOrdered(v, "  ")
		if err != nil {
			t.Fatalf("marshal %s: %v", input, err)
		}
		if string(out) != input {
			t.Fatalf("empty container %s -> %s", input, out)
		}
	}
}
