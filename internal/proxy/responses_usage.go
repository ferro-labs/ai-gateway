package proxy

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/ferro-labs/ai-gateway/providers"
)

// responsesUsageCap bounds how much this reader holds while hunting for usage —
// the whole JSON body on the non-streaming path, or a single SSE frame (the
// terminal one carries the full final response) on the streaming path. Past it,
// usage is left unread and the request is priced as unknown, exactly as an
// ordinary pass-through. A frame never exceeds this in practice; the bound only
// exists so a pathological upstream cannot make the gateway buffer without limit.
const responsesUsageCap = 8 << 20 // 8 MiB

// responsesUsage is the subset of the Responses API usage object the gateway
// prices. cached_tokens is a subset of input_tokens and reasoning_tokens a subset
// of output_tokens (do not add them on top); they are surfaced for accurate
// pricing, not summed in.
type responsesUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	TotalTokens        int64 `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u responsesUsage) toUsage() providers.Usage {
	return providers.Usage{
		PromptTokens:     int(u.InputTokens),
		CompletionTokens: int(u.OutputTokens),
		TotalTokens:      int(u.TotalTokens),
		ReasoningTokens:  int(u.OutputTokensDetails.ReasoningTokens),
		CacheReadTokens:  int(u.InputTokensDetails.CachedTokens),
	}
}

// newResponsesUsageReader wraps rc to extract token usage as the body streams
// through unchanged, writing the result into out. stream selects the SSE
// terminal-event scan (usage rides on response.completed / response.incomplete);
// otherwise the whole JSON body is parsed for its top-level usage at Close. The
// bytes returned to the caller are never altered.
func newResponsesUsageReader(rc io.ReadCloser, stream bool, out *providers.Usage) io.ReadCloser {
	return &responsesUsageReader{rc: rc, stream: stream, out: out}
}

type responsesUsageReader struct {
	rc     io.ReadCloser
	stream bool
	out    *providers.Usage
	buf    bytes.Buffer // whole JSON body (non-stream) or the current SSE line (stream)
	gaveUp bool         // exceeded the cap on the current unit; stop accumulating
}

func (r *responsesUsageReader) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	if n > 0 {
		if r.stream {
			r.scanSSE(p[:n])
		} else if !r.gaveUp {
			r.accumulate(p[:n])
		}
	}
	return n, err
}

// accumulate appends b to buf, giving up on the current unit once it exceeds the
// cap. For the non-stream path buf is the whole body; for the stream path it is
// one line, reset after each newline.
func (r *responsesUsageReader) accumulate(b []byte) {
	if r.buf.Len()+len(b) > responsesUsageCap {
		r.gaveUp = true
		r.buf.Reset()
		return
	}
	r.buf.Write(b)
}

// scanSSE splits streamed bytes into lines and parses each terminal-event data
// frame for usage. OpenAI/xAI Responses SSE puts one single-line JSON per event,
// so splitting on '\n' is sufficient (JSON escapes any internal newline).
func (r *responsesUsageReader) scanSSE(b []byte) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if !r.gaveUp {
				r.accumulate(b)
			}
			return
		}
		if !r.gaveUp {
			r.accumulate(b[:i])
			r.processLine(r.buf.Bytes())
		}
		r.buf.Reset()
		r.gaveUp = false // the cap is per-line on the stream path
		b = b[i+1:]
	}
}

// processLine parses a `data:` frame for response.usage. A cheap substring gate
// avoids decoding the large non-terminal frames, and events whose usage is null
// (response.created / in_progress) leave out untouched, so out ends holding the
// terminal event's usage.
func (r *responsesUsageReader) processLine(line []byte) {
	line = bytes.TrimSpace(line)
	data, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return
	}
	data = bytes.TrimSpace(data)
	if !bytes.Contains(data, []byte(`"usage"`)) {
		return
	}
	var frame struct {
		Response struct {
			Usage *responsesUsage `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &frame); err != nil || frame.Response.Usage == nil {
		return
	}
	*r.out = frame.Response.Usage.toUsage()
}

func (r *responsesUsageReader) Close() error {
	if !r.stream && !r.gaveUp && r.buf.Len() > 0 {
		var body struct {
			Usage *responsesUsage `json:"usage"`
		}
		if err := json.Unmarshal(r.buf.Bytes(), &body); err == nil && body.Usage != nil {
			*r.out = body.Usage.toUsage()
		}
	}
	return r.rc.Close()
}
