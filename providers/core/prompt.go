package core

import (
	"fmt"
	"net/http"
	"strings"
)

// SinglePromptText returns the text a message contributes to a text-only
// upstream, refusing content that cannot be carried as text.
//
// Most callers are upstreams that take one prompt string, which is where the
// name comes from. Bedrock's Nova is not — it takes a message list — but it is
// still text-only, so it shares the guard while keeping its own structure.
//
// Message.UnmarshalJSON folds text parts into Content, so a multipart message's
// text is already there and nothing is lost by reading Content alone. Any OTHER
// part type has no representation in a prompt: an image is not describable as
// the words around it. Dropping it and calling the upstream anyway answers 200
// over content the model never saw, which is the same defect as sending only
// the last message of a conversation — so it is refused instead.
//
// The refusal is a 400 because the caller can act on it: send text, or route to
// a model that accepts images.
func SinglePromptText(provider, model string, msg Message) (string, error) {
	for _, part := range msg.ContentParts {
		if part.Type != ContentTypeText {
			return "", StatusError(provider, http.StatusBadRequest, fmt.Sprintf(
				"model %q accepts text only and cannot express a %q content part; "+
					"send text-only messages, or route to a model that accepts images",
				model, part.Type))
		}
	}
	return msg.Content, nil
}

// JoinMessagesAsPrompt flattens a conversation into the one prompt string an
// upstream that takes a single prompt accepts. One line per turn,
// "<role>: <content>", terminated with a bare "assistant: ":
//
//	system: You are terse.
//	user: Hi
//	assistant: Hello
//	user: Again?
//	assistant:
//
// The trailing cue matters: without it a model tends to continue the caller's
// last turn rather than answer it.
//
// This is lossy but honest — the model reads the role names as text rather than
// as a protocol, yet every message reaches it. That is the distinction worth
// holding onto: a lossy join and a dropped message are not the same failure,
// and only the second returns a completion of a conversation that was never
// sent.
//
// A model carrying its own instruction-tuned template — Bedrock's Llama family
// spells turns in the tokenizer's own markers — builds that template itself and
// calls SinglePromptText for the guard alone. Substituting this generic shape
// there would degrade output, so the guard is shared and the format is not.
func JoinMessagesAsPrompt(provider, model string, msgs []Message) (string, error) {
	var sb strings.Builder
	for _, msg := range msgs {
		text, err := SinglePromptText(provider, model, msg)
		if err != nil {
			return "", err
		}
		sb.WriteString(msg.Role)
		sb.WriteString(": ")
		sb.WriteString(text)
		sb.WriteString("\n")
	}
	sb.WriteString(RoleAssistant + ": ")
	return sb.String(), nil
}
