//go:build live

package live_test

import "testing"

func TestLive_OllamaCloud_Chat(t *testing.T)      { liveChat(t, "ollama-cloud") }
func TestLive_OllamaCloud_Stream(t *testing.T)    { liveStream(t, "ollama-cloud") }
func TestLive_OllamaCloud_Embed(t *testing.T)     { liveEmbed(t, "ollama-cloud") }
func TestLive_OllamaCloud_Discovery(t *testing.T) { liveDiscovery(t, "ollama-cloud") }
