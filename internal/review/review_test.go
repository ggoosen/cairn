package review

import (
	"encoding/json"
	"strings"
	"testing"
)

func fixture() (Bundle, []byte, Response) {
	b := Bundle{Version: 1, Created: "2026-11-03T00:00:00Z", Sources: []Source{{ID: "h1", Revision: "r1", Event: "e1", Role: "handoff", Body: "Build failed without the sqlite_fts5 tag."}, {ID: "m1", Revision: "r2", Event: "e2", Role: "memory", Body: "Run go build."}}}
	for i := range b.Sources {
		b.Sources[i].Hash = Hash([]byte(b.Sources[i].Body))
	}
	raw, _ := json.Marshal(b)
	r := Response{BundleHash: Hash(raw), Summary: "Build instructions need the tag.", Proposals: []Proposal{{Title: "Build with FTS5", Action: "revise", VerificationStatus: "hypothesis", VerificationBasis: "Failure observed; proposed fix not tested in this snapshot.", OutcomeEvidence: []Evidence{}, FutureTest: FutureTest{Trigger: "Next clean build", Procedure: "Build with the required tag", SuccessCriterion: "Build exits zero without rediscovery"}, Target: "m1", BaseRevision: "r2", BaseHash: b.Sources[1].Hash, Body: "Run go build -tags sqlite_fts5.", Reason: "The handoff records a build failure.", Benefit: "Avoid repeat failure.", Audience: "Developers", Counterexamples: "Only one handoff reviewed.", Evidence: []Evidence{{ID: "h1", Quote: "Build failed without the sqlite_fts5 tag."}}}}}
	return b, raw, r
}
func TestValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Bundle, *Response)
	}{
		{"wrong bundle", func(b *Bundle, r *Response) { r.BundleHash = "other" }},
		{"stale base", func(b *Bundle, r *Response) { r.Proposals[0].BaseRevision = "old" }},
		{"invented evidence", func(b *Bundle, r *Response) { r.Proposals[0].Evidence[0].Quote = "Not in the note" }},
		{"unknown source", func(b *Bundle, r *Response) { r.Proposals[0].Evidence[0].ID = "elsewhere" }},
		{"duplicate evidence", func(b *Bundle, r *Response) {
			r.Proposals[0].Evidence = append(r.Proposals[0].Evidence, r.Proposals[0].Evidence[0])
		}},
		{"tampered body", func(b *Bundle, r *Response) { b.Sources[0].Body = "changed" }},
		{"no handoff evidence", func(b *Bundle, r *Response) { r.Proposals[0].Evidence = []Evidence{{ID: "m1", Quote: "Run go build."}} }},
		{"unselected target", func(b *Bundle, r *Response) { r.Proposals[0].Target = "h1" }},
		{"unsupported action", func(b *Bundle, r *Response) { r.Proposals[0].Action = "apply" }},
	}
	b, raw, r := fixture()
	if err := Validate(b, raw, r); err != nil {
		t.Fatal(err)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, raw, r := fixture()
			tc.change(&b, &r)
			if Validate(b, raw, r) == nil {
				t.Fatal("accepted invalid review")
			}
		})
	}
	r.Proposals = []Proposal{}
	if err := Validate(b, raw, r); err != nil {
		t.Fatal(err)
	}
}
func TestStrictDecodeAndRender(t *testing.T) {
	for _, s := range []string{`{"unknown":true}`, `{} {}`, `not-json`} {
		var r Response
		if Decode([]byte(s), &r) == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	b, _, r := fixture()
	r.Proposals[0].Body = "# Approved\n<script>alert(1)</script>\n```"
	md := Render(b, r)
	if strings.Contains(md, "\n# Approved") || strings.Contains(md, "<script>") || !strings.Contains(md, "PENDING HUMAN REVIEW") || !strings.Contains(md, "1 of 1 selected handoff notes") {
		t.Fatal(md)
	}
}

func TestVerificationAndFutureUse(t *testing.T) {
	for _, status := range []string{"observation", "hypothesis", "tested_rule"} {
		t.Run(status, func(t *testing.T) {
			b, raw, r := fixture()
			p := &r.Proposals[0]
			p.VerificationStatus = status
			// An exact outcome quote is structurally checked, not semantically certified.
			p.OutcomeEvidence = []Evidence{{ID: "h1", Quote: b.Sources[0].Body}}
			if err := Validate(b, raw, r); err != nil {
				t.Fatal(err)
			}
			rendered := Render(b, r)
			for _, want := range []string{status, p.VerificationBasis, p.FutureTest.Trigger, p.FutureTest.Procedure, p.FutureTest.SuccessCriterion, "not independent verification", "Supporting outcome evidence"} {
				if !strings.Contains(rendered, want) {
					t.Fatalf("missing %q", want)
				}
			}
		})
	}
	tests := []struct {
		name string
		edit func(*Proposal)
	}{
		{"missing status", func(p *Proposal) { p.VerificationStatus = "" }},
		{"unsupported status", func(p *Proposal) { p.VerificationStatus = "verified" }},
		{"missing basis", func(p *Proposal) { p.VerificationBasis = " " }},
		{"missing outcomes array", func(p *Proposal) { p.OutcomeEvidence = nil }},
		{"tested without outcome", func(p *Proposal) { p.VerificationStatus = "tested_rule" }},
		{"invented outcome", func(p *Proposal) { p.OutcomeEvidence = []Evidence{{ID: "h1", Quote: "Build passed"}} }},
		{"unselected outcome", func(p *Proposal) { p.OutcomeEvidence = []Evidence{{ID: "external", Quote: "Build passed"}} }},
		{"duplicate outcome", func(p *Proposal) {
			e := Evidence{ID: "h1", Quote: "Build failed"}
			p.OutcomeEvidence = []Evidence{e, e}
		}},
		{"missing trigger", func(p *Proposal) { p.FutureTest.Trigger = "" }},
		{"missing procedure", func(p *Proposal) { p.FutureTest.Procedure = "" }},
		{"missing success criterion", func(p *Proposal) { p.FutureTest.SuccessCriterion = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, raw, r := fixture()
			tc.edit(&r.Proposals[0])
			if Validate(b, raw, r) == nil {
				t.Fatal("invalid verification accepted")
			}
		})
	}
}
