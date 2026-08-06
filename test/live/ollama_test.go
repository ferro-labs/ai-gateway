//go:build live

package live_test

import "testing"

func TestLive_Ollama_Chat(t *testing.T)      { liveChat(t, "ollama") }
func TestLive_Ollama_Stream(t *testing.T)    { liveStream(t, "ollama") }
func TestLive_Ollama_Embed(t *testing.T)     { liveEmbed(t, "ollama") }
func TestLive_Ollama_Discovery(t *testing.T) { liveDiscovery(t, "ollama") }
