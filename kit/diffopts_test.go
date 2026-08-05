package chroniclekit

import "testing"

func TestHumanize(t *testing.T) {
	cases := map[string]string{
		"qty":        "Qty",
		"unit_price": "Unit Price",
		"created-at": "Created At",
		"productId":  "Product Id",
		"userID":     "User Id",
		"a":          "A",
		"":           "",
	}
	for in, want := range cases {
		if got := humanize(in); got != want {
			t.Errorf("humanize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLabelFor(t *testing.T) {
	resolver := func(path []string) (string, bool) {
		if len(path) == 2 && path[0] == "lines" && path[1] == "qty" {
			return "order.lines.qty", true // i18n key, used verbatim
		}
		return "", false
	}
	cfg := newDiffConfig([]DiffOption{WithLabels(resolver)})
	if got := labelFor(&cfg, []string{"lines", "qty"}); got != "order.lines.qty" {
		t.Errorf("resolver hit: got %q", got)
	}
	if got := labelFor(&cfg, []string{"unit_price"}); got != "Unit Price" {
		t.Errorf("resolver miss must fall back to humanize: got %q", got)
	}
	bare := newDiffConfig(nil)
	if got := labelFor(&bare, []string{"status"}); got != "Status" {
		t.Errorf("no resolver: got %q", got)
	}
}
