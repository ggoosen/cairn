package views

import (
	"strings"
	"testing"
)

func TestExportRenderParseRoundTrip(t *testing.T) {
	fm := ExportFrontMatter{
		MessageID:      "m-1",
		RevisionID:     "r-1",
		BaseRevisionID: "r-1",
		BodyHash:       strings.Repeat("ab", 32),
		ExportedAt:     "2026-07-12T00:00:00.000000Z",
	}
	body := []byte("# Title\n\nbody with --- inline dashes\n")
	blob, err := RenderExport(fm, body)
	if err != nil {
		t.Fatal(err)
	}
	got, gotBody, err := ParseExport(blob)
	if err != nil {
		t.Fatal(err)
	}
	if got != fm {
		t.Fatalf("front-matter round trip: %+v", got)
	}
	if string(gotBody) != string(body) {
		t.Fatalf("body round trip: %q", gotBody)
	}
}

func TestParseExportRejectsMutations(t *testing.T) {
	cases := map[string]string{
		"no front-matter":  "just a body\n",
		"unterminated":     "---\nmessage_id: x\n",
		"unknown field":    "---\nmessage_id: a\nbase_revision_id: b\nbody_hash: c\ndeclared_priority: 3\n---\nbody",
		"incomplete":       "---\nmessage_id: a\n---\nbody",
		"unparseable yaml": "---\n: : :\n---\nbody",
	}
	for name, content := range cases {
		if _, _, err := ParseExport([]byte(content)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
