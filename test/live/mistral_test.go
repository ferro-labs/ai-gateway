//go:build live

package live_test

import "testing"

func TestLive_Mistral_Chat(t *testing.T)          { liveChat(t, "mistral") }
func TestLive_Mistral_Stream(t *testing.T)        { liveStream(t, "mistral") }
func TestLive_Mistral_Embed(t *testing.T)         { liveEmbed(t, "mistral") }
func TestLive_Mistral_Discovery(t *testing.T)     { liveDiscovery(t, "mistral") }
func TestLive_Mistral_Moderation(t *testing.T)    { liveModerate(t, "mistral") }
func TestLive_Mistral_Transcription(t *testing.T) { liveTranscribe(t, "mistral") }
func TestLive_Mistral_Speech(t *testing.T)        { liveSpeak(t, "mistral") }
