package fixture

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// registerChat wires the streaming chat surface.
//
// Only streaming requests are supported because the CLI contract defines the
// SSE response and the playground always streams.
func registerChat(mux *http.ServeMux, s State, tr *tracer) {
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		in, err := decodeChatRequest(r.Body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, kindInvalidRequest, "malformed request body")
			return
		}
		if in.Stream == nil || !*in.Stream {
			writeErr(w, http.StatusBadRequest, kindInvalidRequest, "stream must be true")
			return
		}
		if in.Model == "" {
			in.Model = modelSonnet
		}
		// The X-Request-ID the middleware minted is the log row's trace_id.
		// Recording it here is what closes the attribution loop the playground
		// depends on: /admin/logs answers with a row carrying this exact id.
		trace := w.Header().Get("X-Request-ID")
		tr.set(trace, in.Model)
		streamChat(w, s, in.Model, trace)
	})
}

type chatInput struct {
	Model  string
	Stream *bool
}

func decodeChatRequest(r io.Reader) (chatInput, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return chatInput{}, fmt.Errorf("chat request must be an object")
	}
	var in chatInput
	sawStream := false
	for dec.More() {
		tok, err = dec.Token()
		if err != nil {
			return chatInput{}, err
		}
		key, ok := tok.(string)
		if !ok {
			return chatInput{}, fmt.Errorf("chat request member must be a string")
		}
		switch key {
		case fieldModel:
			err = dec.Decode(&in.Model)
		case "stream":
			if sawStream {
				return chatInput{}, fmt.Errorf("duplicate stream member")
			}
			sawStream = true
			err = dec.Decode(&in.Stream)
		default:
			err = dec.Decode(&json.RawMessage{})
		}
		if err != nil {
			return chatInput{}, err
		}
	}
	if _, err = dec.Token(); err != nil {
		return chatInput{}, err
	}
	if err = dec.Decode(&struct{}{}); err != io.EOF {
		return chatInput{}, fmt.Errorf("chat request must contain one JSON value")
	}
	return in, nil
}

// streamedText is the reply the fake types out, one delta per element.
var streamedText = []string{"Ferro ", "routed ", "this ", "through ", "the ", "fake ", "gateway."}

// streamChat writes the documented SSE framing: `data: <json>\n\n` lines only,
// no `event:` names, terminated by `data: [DONE]` — except on an errored
// stream, which ends with an error frame and NO [DONE]. That asymmetry is real
// gateway behavior; a fake that smoothed it over would hide the CLI bug it
// exists to catch.
func streamChat(w http.ResponseWriter, s State, model, id string) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, canFlush := w.(http.Flusher)
	// Flush per frame only when a delay is configured (the demo path, where a
	// human watches text arrive). With ChatDelay == 0 the whole stream is
	// written in one shot, so a test that does a single Read observes complete
	// framing instead of racing the flusher.
	live := s.ChatDelay > 0
	created := time.Now().Unix()

	frame := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", b)
		if live && canFlush {
			flusher.Flush()
			time.Sleep(s.ChatDelay)
		}
	}
	chunk := func(choices []map[string]any) map[string]any {
		return map[string]any{
			"id":         id,
			fieldObject:  "chat.completion.chunk",
			fieldCreated: created,
			fieldModel:   model,
			"choices":    choices,
		}
	}
	delta := func(d map[string]any) []map[string]any {
		return []map[string]any{{"index": 0, "delta": d}}
	}

	frame(chunk(delta(map[string]any{"role": "assistant"})))

	for i, word := range streamedText {
		if s.ChatFails && i == 2 {
			// Mid-stream failure: error frame, then the stream just ends.
			frame(map[string]any{fieldError: map[string]any{
				fieldMessage: "upstream error from provider anthropic",
				fieldType:    "stream_error",
				"code":       "stream_error",
			}})
			if canFlush {
				flusher.Flush()
			}
			return
		}
		frame(chunk(delta(map[string]any{"content": word})))
	}

	frame(chunk([]map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}))

	// The usage chunk always arrives (the gateway strips it only on an explicit
	// include_usage:false); choices is [], never null.
	usage := chunk([]map[string]any{})
	usage["usage"] = map[string]any{
		"prompt_tokens":     24,
		"completion_tokens": len(streamedText),
		"total_tokens":      24 + len(streamedText),
	}
	frame(usage)

	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}
}
