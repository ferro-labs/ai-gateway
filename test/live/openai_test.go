//go:build live

package live_test

import "testing"

func TestLive_OpenAI_Chat(t *testing.T)          { liveChat(t, "openai") }
func TestLive_OpenAI_Stream(t *testing.T)        { liveStream(t, "openai") }
func TestLive_OpenAI_Embed(t *testing.T)         { liveEmbed(t, "openai") }
func TestLive_OpenAI_Image(t *testing.T)         { liveImage(t, "openai") }
func TestLive_OpenAI_Discovery(t *testing.T)     { liveDiscovery(t, "openai") }
func TestLive_OpenAI_Moderation(t *testing.T)    { liveModerate(t, "openai") }
func TestLive_OpenAI_Transcription(t *testing.T) { liveTranscribe(t, "openai") }
func TestLive_OpenAI_Speech(t *testing.T)        { liveSpeak(t, "openai") }
