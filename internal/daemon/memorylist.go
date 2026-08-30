package daemon

// D19 — `topic-messages`: enumerate the live messages linked to one or more
// topics, in a stable order, with no ranking whatsoever.
//
// WHY THIS OP EXISTS. The memory-tool facade's `view` on a directory has an
// ENUMERATION contract — "here are the files in this directory" — and Cairn had
// no op that answers it. Every existing read is a RETRIEVAL: search ranks and
// cuts to k, digest ranks and cuts to a budget, thread renders bodies. Serving
// a listing out of any of them would have meant deciding which files exist by
// score, which is precisely the quiet cleverness D19 forbids ("do not smuggle
// ranking into a call whose contract is enumeration").
//
// So this is deliberately the dullest read in the daemon: topic names in,
// message rows out, ordered by (created_at, message_id), nothing dropped except
// by the caller's own explicit limit and nothing reordered. It computes no
// score, opens no interaction, and touches no telemetry — an enumeration is not
// a retrieval, and recording it as one would poison the outcome statistics that
// calibrate ranking.
//
// It reads through the projection's EXISTING exported queries (ScopeMessageIDs
// is the same resolver `cairn search --topic` uses, MessageInfo the same one
// `peek` uses), so there is no second notion of what a topic contains and no
// new SQL to keep in step with the schema.

import (
	"sort"
)

// TopicMessage is one enumerated message: enough to render a directory entry,
// and nothing that would require reading a body.
type TopicMessage struct {
	MessageID string `json:"message_id"`
	Topic     string `json:"topic,omitempty"`
	ByteLen   int64  `json:"byte_len"`
	TextClass string `json:"text_class,omitempty"`
	Sender    string `json:"sender,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	Retracted bool   `json:"retracted,omitempty"`
}

// TopicMessages enumerates the live messages linked to every named topic.
//
// A topic that does not exist is an ERROR, not an empty list — ScopeMessageIDs
// refuses an unknown scope for the same reason (a scope that silently matches
// nothing is indistinguishable from a directory that is genuinely empty).
// Retracted messages are omitted: a retraction is how this mesh says a message
// is no longer current, and an enumeration that lists them would contradict
// every other read.
//
// limit bounds the result; the total BEFORE the limit is returned as well, so a
// caller can report the truncation rather than pass a short list off as
// complete.
func (d *Daemon) TopicMessages(topics []string, limit int) (out []TopicMessage, total int, err error) {
	if len(topics) == 0 {
		return nil, 0, nil
	}
	ids, err := d.proj.ScopeMessageIDs(topics, "", "")
	if err != nil {
		return nil, 0, err
	}
	meta, err := d.proj.ResultMeta(keysOf(ids))
	if err != nil {
		return nil, 0, err
	}
	for id := range ids {
		info, ierr := d.proj.MessageInfo(id)
		if ierr != nil || info == nil || info.Retracted {
			continue
		}
		topic := ""
		if names := meta[id].Topics; len(names) > 0 {
			// The scope may name several topics; report the one this message
			// is filed under that the CALLER asked for, so a listing of
			// "memory/x" never labels an entry with an unrelated topic the
			// message also carries.
			topic = firstMatch(names, topics)
		}
		out = append(out, TopicMessage{
			MessageID: info.MessageID, Topic: topic, ByteLen: info.BodyLen,
			TextClass: info.TextClass, Sender: info.Sender, CreatedAt: info.CreatedAt,
		})
	}
	// Stable, content-independent order: oldest first, message id breaking ties.
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].MessageID < out[j].MessageID
	})
	total = len(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func firstMatch(have, want []string) string {
	for _, w := range want {
		for _, h := range have {
			if h == w {
				return h
			}
		}
	}
	return ""
}
