package main

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"

	"github.com/ferro-labs/ai-gateway/internal/httpclient"
	"github.com/ferro-labs/ai-gateway/providers"
)

// proxyHandler returns an http.HandlerFunc that transparently forwards
// any /v1/* request to the matching upstream provider.
//
// This enables pass-through for endpoints the gateway does not handle
// natively (e.g. /v1/files, /v1/batches, /v1/fine_tuning, /v1/responses,
// /v1/audio/*, /v1/images/edits, /v1/realtime, etc.) while still injecting
// the correct provider authentication headers.
//
// Provider resolution order:
//  1. X-Provider request header (e.g. "X-Provider: openai")
//  2. "model" field in the JSON request body
//
// If neither resolves a provider, a 400 is returned with instructions.
func proxyHandler(registry *providers.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := resolveProvider(r, registry)
		if !ok {
			writeOpenAIError(w, http.StatusBadRequest,
				`no provider resolved; set the X-Provider header (e.g. "X-Provider: openai") or include a "model" field in the request body`,
				"invalid_request_error",
				"provider_not_resolved",
			)
			return
		}

		pp, canProxy := p.(providers.ProxiableProvider)
		if !canProxy {
			writeOpenAIError(w, http.StatusNotImplemented,
				"provider "+p.Name()+" does not support proxy pass-through",
				"invalid_request_error",
				"proxy_not_supported",
			)
			return
		}

		target, err := url.Parse(pp.BaseURL())
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "invalid provider base URL: "+err.Error(), "server_error", "internal_error")
			return
		}

		authHeaders := pp.AuthHeaders()
		providerName := p.Name()

		proxy := &httputil.ReverseProxy{
			Transport: httpclient.SharedTransport(),
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(target)
				pr.Out.Header.Del("X-Provider")
				pr.Out.Header.Del("Authorization")
				for k, v := range authHeaders {
					pr.Out.Header.Set(k, v)
				}
				pr.SetXForwarded()
			},
			ModifyResponse: func(resp *http.Response) error {
				resp.Header.Set("X-Gateway-Provider", providerName)
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
				http.Error(w, "proxy error: "+err.Error(), http.StatusBadGateway)
			},
		}

		proxy.ServeHTTP(w, r)
	}
}

// resolveProvider determines which provider should receive the request.
// It checks the X-Provider header first, then falls back to model-based lookup
// by peeking at (and restoring) the JSON request body.
func resolveProvider(r *http.Request, registry *providers.Registry) (providers.Provider, bool) {
	// 1. Explicit header takes precedence.
	if name := r.Header.Get("X-Provider"); name != "" {
		return registry.Get(name)
	}

	// 2. Try to extract "model" from the request body.
	if r.Body == nil || r.ContentLength == 0 {
		return nil, false
	}

	model, err := extractTopLevelModel(r)
	if err != nil || model == "" {
		return nil, false
	}
	return registry.FindByModel(model)
}

func extractTopLevelModel(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", io.EOF
	}

	scanner := newTopLevelModelScanner(r.Body)
	model, err := scanner.extract()
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(scanner.captured.Bytes()), r.Body))
	if err != nil {
		return "", err
	}
	return model, nil
}

type topLevelModelScanner struct {
	reader   *bufio.Reader
	captured bytes.Buffer
}

func newTopLevelModelScanner(r io.Reader) *topLevelModelScanner {
	s := &topLevelModelScanner{}
	s.reader = bufio.NewReaderSize(io.TeeReader(r, &s.captured), 4096)
	return s
}

func (s *topLevelModelScanner) extract() (string, error) {
	tok, err := s.nextNonSpaceByte()
	if err != nil {
		return "", err
	}
	if tok != '{' {
		return "", nil
	}

	for {
		tok, err = s.nextNonSpaceByte()
		if err != nil {
			if err == io.EOF {
				return "", nil
			}
			return "", err
		}
		if tok == '}' {
			return "", nil
		}
		if tok != '"' {
			return "", nil
		}

		key, err := s.readJSONString()
		if err != nil {
			return "", err
		}
		tok, err = s.nextNonSpaceByte()
		if err != nil {
			return "", err
		}
		if tok != ':' {
			return "", nil
		}

		if key == "model" {
			tok, err := s.nextNonSpaceByte()
			if err != nil {
				return "", err
			}
			if tok != '"' {
				if err := s.skipJSONValue(tok); err != nil {
					return "", err
				}
				return "", nil
			}
			return s.readJSONString()
		}

		tok, err = s.nextNonSpaceByte()
		if err != nil {
			return "", err
		}
		if err := s.skipJSONValue(tok); err != nil {
			return "", err
		}

		tok, err = s.nextNonSpaceByte()
		if err != nil {
			if err == io.EOF {
				return "", nil
			}
			return "", err
		}
		switch tok {
		case ',':
			continue
		case '}':
			return "", nil
		default:
			return "", nil
		}
	}
}

func (s *topLevelModelScanner) nextNonSpaceByte() (byte, error) {
	for {
		b, err := s.reader.ReadByte()
		if err != nil {
			return 0, err
		}
		switch b {
		case ' ', '\n', '\r', '\t':
			continue
		default:
			return b, nil
		}
	}
}

func (s *topLevelModelScanner) readJSONString() (string, error) {
	buf := make([]byte, 0, 32)
	escaped := false
	for {
		b, err := s.reader.ReadByte()
		if err != nil {
			return "", err
		}
		if escaped {
			buf = append(buf, '\\', b)
			escaped = false
			continue
		}
		switch b {
		case '\\':
			escaped = true
		case '"':
			if bytes.IndexByte(buf, '\\') == -1 {
				return string(buf), nil
			}
			return strconv.Unquote(`"` + string(buf) + `"`)
		default:
			buf = append(buf, b)
		}
	}
}

func (s *topLevelModelScanner) skipJSONValue(first byte) error {
	switch first {
	case '"':
		_, err := s.readJSONString()
		return err
	case '{', '[':
		return s.skipComposite(first)
	default:
		return s.skipScalar()
	}
}

func (s *topLevelModelScanner) skipComposite(open byte) error {
	var closeCh byte
	switch open {
	case '{':
		closeCh = '}'
	case '[':
		closeCh = ']'
	default:
		return nil
	}

	depth := 1
	for depth > 0 {
		b, err := s.reader.ReadByte()
		if err != nil {
			return err
		}
		switch b {
		case '"':
			if _, err := s.readJSONString(); err != nil {
				return err
			}
		case open:
			depth++
		case closeCh:
			depth--
		case '{':
			if open != '{' {
				if err := s.skipComposite(b); err != nil {
					return err
				}
			}
		case '[':
			if open != '[' {
				if err := s.skipComposite(b); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *topLevelModelScanner) skipScalar() error {
	for {
		b, err := s.reader.ReadByte()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch b {
		case ',', '}', ']', ' ', '\n', '\r', '\t':
			if err := s.reader.UnreadByte(); err != nil {
				return err
			}
			return nil
		}
	}
}
