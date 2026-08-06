//go:build live

package live_test

import "testing"

func TestLive_Together_Chat(t *testing.T)          { liveChat(t, "together") }
func TestLive_Together_Stream(t *testing.T)        { liveStream(t, "together") }
func TestLive_Together_Embed(t *testing.T)         { liveEmbed(t, "together") }
func TestLive_Together_Discovery(t *testing.T)     { liveDiscovery(t, "together") }
func TestLive_Together_Image(t *testing.T)         { liveImage(t, "together") }
func TestLive_Together_Rerank(t *testing.T)        { liveRerank(t, "together") }
func TestLive_Together_Transcription(t *testing.T) { liveTranscribe(t, "together") }
func TestLive_Together_Speech(t *testing.T)        { liveSpeak(t, "together") }
