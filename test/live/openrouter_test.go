//go:build live

package live_test

import "testing"

func TestLive_OpenRouter_Chat(t *testing.T)      { liveChat(t, "openrouter") }
func TestLive_OpenRouter_Stream(t *testing.T)    { liveStream(t, "openrouter") }
func TestLive_OpenRouter_Embed(t *testing.T)     { liveEmbed(t, "openrouter") }
func TestLive_OpenRouter_Discovery(t *testing.T) { liveDiscovery(t, "openrouter") }
