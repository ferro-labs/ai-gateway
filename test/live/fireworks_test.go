//go:build live

package live_test

import "testing"

func TestLive_Fireworks_Chat(t *testing.T)          { liveChat(t, "fireworks") }
func TestLive_Fireworks_Stream(t *testing.T)        { liveStream(t, "fireworks") }
func TestLive_Fireworks_Embed(t *testing.T)         { liveEmbed(t, "fireworks") }
func TestLive_Fireworks_Discovery(t *testing.T)     { liveDiscovery(t, "fireworks") }
func TestLive_Fireworks_Transcription(t *testing.T) { liveTranscribe(t, "fireworks") }
