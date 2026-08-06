package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	aigateway "github.com/ferro-labs/ai-gateway"
	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/providers"
)

// transcriptionMultipartMemory bounds how much of the multipart form is buffered
// in memory before Go spills the rest to a temp file.
const transcriptionMultipartMemory = 10 << 20 // 10 MiB

// audioMaxRequestBytes caps the audio upload body. It is larger than the JSON
// surfaces' default because a multipart audio file is larger; OpenAI's whisper
// accepts 25 MiB. Applied here (not via the shared MaxRequestBody middleware)
// because the audio routes need a higher cap than the /v1/* group's, and the
// body must be bounded before the multipart parser reads it.
const audioMaxRequestBytes = 25 << 20 // 25 MiB

// Transcriptions handles POST /v1/audio/transcriptions (translate=false) and
// POST /v1/audio/translations (translate=true). It parses the multipart audio
// upload and routes to the first registered TranscriptionProvider that serves
// the requested model.
func Transcriptions(gw *aigateway.Gateway, translate bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, audioMaxRequestBytes)
		if err := r.ParseMultipartForm(transcriptionMultipartMemory); err != nil {
			writeAudioBodyError(w, err, "invalid multipart form")
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			writeAudioBodyError(w, err, "file is required")
			return
		}
		defer func() { _ = file.Close() }()

		model := r.FormValue("model")
		if model == "" {
			apierror.WriteOpenAI(w, http.StatusBadRequest, "model is required", "invalid_request_error", "invalid_request")
			return
		}

		audio, err := io.ReadAll(file)
		if err != nil {
			writeAudioBodyError(w, err, "failed to read audio file")
			return
		}

		req := providers.TranscriptionRequest{
			Model:          model,
			File:           audio,
			Filename:       header.Filename,
			Language:       r.FormValue("language"),
			Prompt:         r.FormValue("prompt"),
			ResponseFormat: r.FormValue("response_format"),
			Translate:      translate,
		}
		if t := r.FormValue("temperature"); t != "" {
			if v, perr := strconv.ParseFloat(t, 64); perr == nil {
				req.Temperature = &v
			}
		}

		resp, err := gw.Transcribe(r.Context(), req)
		if err != nil {
			apierror.WriteRouteError(w, err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// writeAudioBodyError maps a multipart/read failure to 413 for a body-limit hit
// or 400 otherwise.
func writeAudioBodyError(w http.ResponseWriter, err error, msg string) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		apierror.WriteOpenAI(w, http.StatusRequestEntityTooLarge, "request body too large", "invalid_request_error", "request_too_large")
		return
	}
	apierror.WriteOpenAI(w, http.StatusBadRequest, msg, "invalid_request_error", "invalid_request")
}
