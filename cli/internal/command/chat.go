package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ferro-labs/ai-gateway/cli/internal/api"
)

const (
	// attributionRetryDelay is how long ferro waits before its single retry of
	// the trace lookup. The request-log row is written after the stream ends,
	// so the first lookup routinely races it and finds nothing.
	attributionRetryDelay = 500 * time.Millisecond

	// attributionSkew widens the log window backwards past the moment the
	// request started. created_at is stamped by the *gateway's* clock and the
	// window is filtered against it, so without a margin a client running a
	// second fast would never match its own row.
	// It is a fixed margin, not clock synchronization — the row is then matched
	// by exact trace id, so a wider window costs nothing but rows scanned.
	// Matches internal/tui's attributionSlack. The TUI found empirically that a
	// one-minute margin was not always enough, and two callers of the same endpoint
	// disagreeing about clock tolerance is a bug waiting for a slow gateway.
	attributionSkew = 2 * time.Minute
)

// stdinIsTTY reports whether stdin is a terminal, which is how `ferro chat`
// with no arguments tells "the operator forgot the prompt" from "the prompt is
// being piped in". It is a variable so a test can state which of the two it is
// exercising; nothing else reassigns it.
var stdinIsTTY = isTerminalStdin

func isTerminalStdin() bool { return isTTY(os.Stdin) }

// chatResult is the frozen --format json contract. Provider and cost are
// omitted rather than zeroed when attribution found nothing: a consumer must be
// able to tell "served by nobody knows what" from "served by a provider named
// empty string", and $0.0000 is a real price some requests genuinely have.
type chatResult struct {
	Model     string     `json:"model"`
	Content   string     `json:"content"`
	Usage     *api.Usage `json:"usage"`
	RequestID string     `json:"request_id"`
	Provider  string     `json:"provider,omitempty"`
	CostUSD   *float64   `json:"cost_usd,omitempty"`
}

func newChatCmd() *cobra.Command {
	var model, system string
	cmd := &cobra.Command{
		Use:   "chat [flags] <prompt>...",
		Short: "Send one prompt through the gateway and stream the answer",
		Long: "chat sends a single prompt to a model through the gateway and streams the\n" +
			"answer to stdout as it arrives. The prompt is the arguments joined, or\n" +
			"stdin when there are none and stdin is not a terminal.\n\n" +
			"stdout carries the answer and nothing else — no ANSI, no markdown\n" +
			"rendering — so `ferro chat ... > answer.txt` captures exactly what the\n" +
			"model said. What served it, what it cost, and any failure go to stderr.\n" +
			"A stream that fails or ends mid-answer exits 1: a truncated answer that\n" +
			"exited 0 would be indistinguishable from a complete one.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validated here rather than left to the gateway: an empty value
			// satisfies cobra's required-flag check, which only asks whether the
			// flag was set.
			model = strings.TrimSpace(model)
			if model == "" {
				return errors.New("--model is required and must name a model this gateway serves (ferro models)")
			}
			prompt, err := readPrompt(cmd, args)
			if err != nil {
				return err
			}
			return runChat(cmd, model, system, prompt)
		},
	}
	f := cmd.Flags()
	f.StringVar(&model, "model", "", "model to send the prompt to (required)")
	f.StringVar(&system, "system", "", "system message sent ahead of the prompt")
	_ = cmd.MarkFlagRequired("model")
	return cmd
}

// readPrompt takes the prompt from the arguments, or from stdin when there are
// none. Reading a terminal would block forever on input the operator does not
// know is wanted, so that case is an error naming both ways to supply one.
func readPrompt(cmd *cobra.Command, args []string) (string, error) {
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	if stdinIsTTY() {
		return "", errors.New(`no prompt: pass one as arguments (ferro chat "explain X" --model M) or pipe it on stdin`)
	}
	b, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return "", fmt.Errorf("read prompt from stdin: %w", err)
	}
	prompt := strings.TrimSpace(string(b))
	if prompt == "" {
		return "", errors.New("no prompt: stdin was empty")
	}
	return prompt, nil
}

func runChat(cmd *cobra.Command, model, system, prompt string) error {
	d := deps(cmd)
	ctx := cmd.Context()

	msgs := make([]api.ChatMessage, 0, 2)
	if system != "" {
		msgs = append(msgs, api.ChatMessage{Role: "system", Content: system})
	}
	msgs = append(msgs, api.ChatMessage{Role: "user", Content: prompt})

	started := time.Now().Add(-attributionSkew)
	st, err := d.Client.StreamChat(ctx, api.ChatRequest{Model: model, Messages: msgs})
	if err != nil {
		return err
	}
	// Cancel releases the reader goroutine and the connection even after a
	// clean end, and is safe to call twice.
	defer st.Cancel()

	// Table format is the streaming, human-facing mode; json and yaml collect
	// the whole answer and emit one document, so nothing but that document may
	// touch stdout.
	streaming := d.Printer.Format == FormatTable

	res := chatResult{Model: model, RequestID: st.RequestID}
	var content strings.Builder
	var streamErr *api.Error
	done := false

	for ev := range st.Events {
		switch {
		case ev.Err != nil:
			streamErr = ev.Err
		case ev.Done:
			done = true
		case ev.Chunk != nil:
			// The gateway names the model that actually served the request;
			// fall back to the requested one only when it does not.
			if ev.Chunk.Model != "" {
				res.Model = ev.Chunk.Model
			}
			if ev.Chunk.Usage != nil {
				res.Usage = ev.Chunk.Usage
			}
			for _, ch := range ev.Chunk.Choices {
				// reasoning_content and tool_calls are deliberately not
				// printed — stdout is the answer, and anything else on it
				// corrupts the one channel a pipeline reads.
				if ch.Delta.Content == "" {
					continue
				}
				content.WriteString(ch.Delta.Content)
				if streaming {
					// In production Out is os.Stdout, an *os.File: every write
					// is a syscall, so deltas appear as they arrive with no
					// flushing of ours to arrange.
					_, _ = fmt.Fprint(d.Printer.Out, ch.Delta.Content)
				}
			}
		}
	}
	res.Content = content.String()

	if streaming && res.Content != "" && !strings.HasSuffix(res.Content, "\n") {
		// Models rarely end on a newline; without this the shell prompt lands
		// mid-answer. It is a terminator, not a rendering.
		_, _ = fmt.Fprintln(d.Printer.Out)
	}

	// Checked before the error paths: an interrupt can surface either as a
	// silent close or as a truncation the reader noticed first, and both mean
	// the same thing — the operator ended their own request.
	if !done && ctx.Err() != nil {
		d.Printer.Warn("interrupted — output is truncated")
		if !streaming {
			if err := d.printStructured(res); err != nil {
				return err
			}
			return errors.New("chat interrupted — structured output is truncated")
		}
		return nil
	}
	if streamErr != nil {
		return streamFailure(streamErr)
	}
	if !done {
		// Belt and braces: internal/api synthesizes stream_incomplete for this,
		// so arriving here means even that frame could not be delivered. Same
		// fact, same non-zero exit.
		return streamFailure(&api.Error{Code: api.CodeStreamIncomplete})
	}

	if entry := attribute(ctx, d.Client, st.RequestID, started); entry != nil {
		res.Provider = entry.Provider
		res.CostUSD = entry.CostUSD
	}
	if !streaming {
		return d.printStructured(res)
	}
	printChatMeta(d, res)
	return nil
}

// streamFailure turns a terminal stream code into the sentence an operator can
// act on. All three exit non-zero: the answer on stdout is incomplete in every
// one of them, and only the exit code can say so to a pipeline.
func streamFailure(e *api.Error) error {
	switch e.Code {
	case api.CodeStreamTimeout:
		// The bound itself is the gateway's, configurable there and not
		// reported on the wire, so naming a duration here would be a guess an
		// operator reads during the incident it is wrong in.
		return errors.New("stream idled out — the gateway closed it after its idle bound elapsed")
	case api.CodeStreamIncomplete:
		return errors.New("stream ended mid-answer — output is truncated")
	default:
		if e.Message == "" {
			return fmt.Errorf("stream failed: %s", e.Error())
		}
		return fmt.Errorf("stream failed: %s", e.Message)
	}
}

// attribute resolves which provider served the answer and what it cost. A
// streamed body names neither, so the only path is the request log — which is
// written after the stream ends, hence the one retry.
//
// Every failure answers nil: attribution is a detail printed beside an answer
// the operator already has, and failing the command over its absence would turn
// a cosmetic gap into a broken chat.
func attribute(ctx context.Context, c *api.Client, traceID string, since time.Time) *api.LogEntry {
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(attributionRetryDelay):
			}
		}
		entry, err := c.TraceAttribution(ctx, traceID, since)
		if err != nil || entry != nil {
			return entry
		}
	}
	return nil
}

// printChatMeta writes the one narrative line a chat adds, to stderr. Each
// segment is omitted when the fact behind it is absent rather than rendered as
// a zero or an empty name — an invented provider is worse than a shorter line.
func printChatMeta(d *runtimeDeps, res chatResult) {
	seg := []string{res.Model}
	if res.Provider != "" {
		seg = append(seg, res.Provider)
	}
	if res.Usage != nil {
		seg = append(seg, fmt.Sprintf("%d in / %d out", res.Usage.PromptTokens, res.Usage.CompletionTokens))
	}
	if res.CostUSD != nil {
		seg = append(seg, fmtCost(res.CostUSD))
	}
	_, _ = fmt.Fprintln(d.Printer.Err, strings.Join(seg, " "+d.Printer.glyph("·", "|")+" "))
}
