//go:build live

package live_test

import "testing"

func TestLive_DeepInfra_Chat(t *testing.T)          { liveChat(t, "deepinfra") }
func TestLive_DeepInfra_Stream(t *testing.T)        { liveStream(t, "deepinfra") }
func TestLive_DeepInfra_Embed(t *testing.T)         { liveEmbed(t, "deepinfra") }
func TestLive_DeepInfra_Discovery(t *testing.T)     { liveDiscovery(t, "deepinfra") }
func TestLive_DeepInfra_Image(t *testing.T)         { liveImage(t, "deepinfra") }
func TestLive_DeepInfra_Rerank(t *testing.T)        { liveRerank(t, "deepinfra") }
func TestLive_DeepInfra_Transcription(t *testing.T) { liveTranscribe(t, "deepinfra") }
func TestLive_DeepInfra_Speech(t *testing.T)        { liveSpeak(t, "deepinfra") }
