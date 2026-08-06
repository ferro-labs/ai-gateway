//go:build live

package live_test

import "testing"

func TestLive_SambaNova_Chat(t *testing.T)          { liveChat(t, "sambanova") }
func TestLive_SambaNova_Stream(t *testing.T)        { liveStream(t, "sambanova") }
func TestLive_SambaNova_Embed(t *testing.T)         { liveEmbed(t, "sambanova") }
func TestLive_SambaNova_Discovery(t *testing.T)     { liveDiscovery(t, "sambanova") }
func TestLive_SambaNova_Transcription(t *testing.T) { liveTranscribe(t, "sambanova") }
