//go:build integration
// +build integration

package http_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// samplePNG returns a real, valid 2x2 PNG encoded by image/png — genuine image
// bytes, not a placeholder string — so the image paths are exercised with a
// decodable image the way a provider would actually receive/return one.
func samplePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{G: 255, A: 255})
	img.Set(0, 1, color.RGBA{B: 255, A: 255})
	img.Set(1, 1, color.RGBA{R: 255, G: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode sample PNG: %v", err)
	}
	return buf.Bytes()
}

// sampleWAV returns a real, valid 16-bit mono 8 kHz PCM WAV — a genuine, playable
// audio file built to the RIFF/WAVE spec, so the multipart upload path carries
// real audio bytes rather than a placeholder string.
func sampleWAV() []byte {
	const sampleRate = 8000
	samples := []int16{0, 4096, 8192, 4096, 0, -4096, -8192, -4096, 0, 4096, 8192, 4096, 0, -4096, -8192, -4096}
	dataSize := len(samples) * 2

	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize)) //nolint:gosec // fixed tiny WAV, dataSize is a small constant
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16)) // Subchunk1Size
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))  // PCM
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))  // mono
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(sampleRate*2)) // byte rate (mono, 16-bit)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(2))            // block align
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16))           // bits per sample
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(dataSize)) //nolint:gosec // fixed tiny WAV, dataSize is a small constant
	for _, s := range samples {
		_ = binary.Write(&buf, binary.LittleEndian, s)
	}
	return buf.Bytes()
}

// TestTranscriptions_RealWAVBytesRoundTrip uploads a real WAV through the
// multipart audio path and asserts the provider received the EXACT bytes and
// filename — proving the handler streams a real audio file to the provider
// without corruption, which a 5-byte placeholder never exercised.
func TestTranscriptions_RealWAVBytesRoundTrip(t *testing.T) {
	env := newTestServer(t)
	audio := sampleWAV()

	var gotBytes []byte
	var gotFilename string
	env.Stub.TranscribeHook = func(_ context.Context, req core.TranscriptionRequest) (*core.TranscriptionResponse, error) {
		gotBytes = append([]byte(nil), req.File...)
		gotFilename = req.Filename
		return &core.TranscriptionResponse{Text: "transcribed"}, nil
	}

	body, contentType := buildAudioMultipart(t, stubTranscriptionModel, "sample.wav", audio)
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/audio/transcriptions", body)
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", contentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/audio/transcriptions: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	if !bytes.Equal(gotBytes, audio) {
		t.Errorf("provider received %d bytes, want the %d-byte sample WAV verbatim", len(gotBytes), len(audio))
	}
	if gotFilename != "sample.wav" {
		t.Errorf("provider filename = %q, want sample.wav", gotFilename)
	}
	// The bytes that arrived are a real WAV (the RIFF/WAVE header survived intact).
	if len(gotBytes) < 12 || string(gotBytes[0:4]) != "RIFF" || string(gotBytes[8:12]) != "WAVE" {
		t.Error("uploaded bytes reaching the provider are not a valid WAV")
	}
}

// TestImages_RealPNGRoundTrips has the provider return a real PNG (base64) and
// asserts the response's b64_json decodes back to a valid image — proving a real
// generated image survives the response path, which a "stub-image" string never
// checked.
func TestImages_RealPNGRoundTrips(t *testing.T) {
	env := newTestServer(t)
	pngBytes := samplePNG(t)
	env.Stub.GenerateImageHook = func(_ context.Context, _ core.ImageRequest) (*core.ImageResponse, error) {
		return &core.ImageResponse{
			Data: []core.GeneratedImage{{B64JSON: base64.StdEncoding.EncodeToString(pngBytes)}},
		}, nil
	}

	resp := postImages(t, env.Server.URL, `{"model":"`+stubImageModel+`","prompt":"a red square"}`)
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Data) == 0 || result.Data[0].B64JSON == "" {
		t.Fatal("expected a b64_json image in the response")
	}
	raw, err := base64.StdEncoding.DecodeString(result.Data[0].B64JSON)
	if err != nil {
		t.Fatalf("response b64_json is not valid base64: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("response image is not a decodable PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 2 || b.Dy() != 2 {
		t.Errorf("decoded image is %dx%d, want the 2x2 sample", b.Dx(), b.Dy())
	}
}

// TestChat_RealImageInputPassesThrough sends a chat request carrying a real PNG
// as a base64 image_url data URI and asserts the provider received it as an
// image content part with the exact data URI — proving the gateway parses
// multimodal image INPUT and forwards it intact.
func TestChat_RealImageInputPassesThrough(t *testing.T) {
	env := newTestServer(t)
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(samplePNG(t))

	var gotParts []core.ContentPart
	env.Stub.CompleteHook = func(_ context.Context, req core.Request) (*core.Response, error) {
		if len(req.Messages) > 0 {
			gotParts = req.Messages[len(req.Messages)-1].ContentParts
		}
		return &core.Response{
			ID: "resp-vision", Object: "chat.completion", Model: req.Model, Provider: "stub",
			Choices: []core.Choice{{Index: 0, Message: core.Message{Role: "assistant", Content: "a square"}, FinishReason: "stop"}},
		}, nil
	}

	body := `{"model":"` + stubModelName + `","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"what is this?"},` +
		`{"type":"image_url","image_url":{"url":"` + dataURI + `"}}]}]}`
	req := newTestRequest(t, "POST", env.Server.URL+"/v1/chat/completions", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+testMasterKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer closeTestBody(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var gotImageURL string
	for _, p := range gotParts {
		if p.Type == "image_url" && p.ImageURL != nil {
			gotImageURL = p.ImageURL.URL
		}
	}
	if gotImageURL != dataURI {
		t.Errorf("provider received image_url = %q, want the sample PNG data URI to pass through intact", gotImageURL)
	}
}
