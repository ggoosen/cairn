package daemon

// R56 self-bootstrapping onboarding record (server side).
//
// The daemon LOCATES the authoritative onboarding record for a view and runs
// the security core (internal/onboarding.Verify) against it, returning either a
// verified, whitelisted config or a refusal reason. It never APPLIES anything —
// application (the R25 local subscribe + the delimited instructions-file
// rewrite) is the CLI's job, so the bounded effects stay client-side and
// observable. The record's authorship gate is sender_principal == "operator"
// (the tier-1/full operator; agents can never publish under it — R21/R22), on
// top of the log's per-event signature + membership verification (R5).

import (
	"fmt"

	"github.com/ggoosen/cairn/internal/config"
	"github.com/ggoosen/cairn/internal/onboarding"
)

// OnboardingResult is the located + verified onboarding record for a view.
type OnboardingResult struct {
	Found      bool               `json:"found"`
	Topic      string             `json:"topic,omitempty"`
	MessageID  string             `json:"message_id,omitempty"`
	RevisionID string             `json:"revision_id,omitempty"`
	Sender     string             `json:"sender,omitempty"`
	Verified   bool               `json:"verified"`
	Refusal    string             `json:"refusal,omitempty"`
	Config     *onboarding.Config `json:"config,omitempty"`
}

// onboardingTopics is the lookup order: the view-specific record wins over the
// mesh-wide default.
func onboardingTopics(view string) []string {
	return []string{"cairn/onboarding/" + view, "cairn/onboarding"}
}

// OnboardingRecord finds the latest record on the view's onboarding topic
// (view-specific first, then the mesh-wide default), reads its body, and runs
// the R56 verification. A missing record is Found:false (not an error); a found
// record that fails the authorship/schema gate is Found:true, Verified:false
// with a Refusal reason (still readable as data — the body is never returned as
// config).
func (d *Daemon) OnboardingRecord(view string) (*OnboardingResult, error) {
	if !validViewName(view) {
		return nil, fmt.Errorf("invalid view %q", view)
	}
	// Locate the latest OPERATOR-authored record only: a non-operator writer
	// cannot shadow it by posting newer noise to the topic (defence in depth on
	// bound 1 — Verify below is still the authoritative authorship gate).
	var topic, msgID string
	for _, t := range onboardingTopics(view) {
		id, err := d.proj.LatestTopicMessageBySender(t, config.SignalOperatorPrincipal)
		if err != nil {
			return nil, err
		}
		if id != "" {
			topic, msgID = t, id
			break
		}
	}
	if msgID == "" {
		return &OnboardingResult{Found: false}, nil
	}

	info, err := d.proj.MessageInfo(msgID)
	if err != nil {
		return nil, err
	}
	res := &OnboardingResult{
		Found:      true,
		Topic:      topic,
		MessageID:  msgID,
		RevisionID: info.HeadRevisionID,
		Sender:     info.Sender,
	}
	body, err := d.store.Get(info.BodyHash)
	if err != nil {
		return nil, fmt.Errorf("onboarding record body unreadable: %w", err)
	}
	cfg, verr := onboarding.Verify(info.Sender, string(body))
	if verr != nil {
		res.Refusal = verr.Error()
		return res, nil
	}
	cfg.Revision = info.HeadRevisionID
	if cfg.View == "" {
		cfg.View = view
	}
	// Defence in depth (bound 3a): a record's declared view must match the view
	// being onboarded — an operator record for view A can never reconfigure B.
	if cfg.View != view {
		res.Refusal = fmt.Sprintf("record declares view %q but was requested for %q: not applied", cfg.View, view)
		return res, nil
	}
	res.Verified = true
	res.Config = &cfg
	return res, nil
}
