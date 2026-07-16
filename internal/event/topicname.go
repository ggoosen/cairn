package event

import "regexp"

// TopicNamePattern is the normative topic-name rule from the event schema
// (build/schemas/p0-events.schema.json, topic.create → name). It is defined
// here — the lowest package that both the daemon (write boundaries) and the
// projection (ingest boundary) already import — so R53's "validate at EVERY
// write/ingest boundary" is enforced against a SINGLE source of truth rather
// than a pattern re-typed per call site.
const TopicNamePattern = `^[a-z0-9][a-z0-9/_-]*$`

var topicNameRe = regexp.MustCompile(TopicNamePattern)

// ValidTopicName reports whether name conforms to the schema pattern. A topic
// name is untrusted, peer-authorable data (it rides in on topic.create and is
// rendered into agent-facing views); every path that creates or ingests one
// MUST gate on this before the name becomes durable or projected (R53).
func ValidTopicName(name string) bool { return topicNameRe.MatchString(name) }
