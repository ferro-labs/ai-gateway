//go:build live

package live_test

import "testing"

func TestLive_Qwen_Chat(t *testing.T)      { liveChat(t, "qwen") }
func TestLive_Qwen_Stream(t *testing.T)    { liveStream(t, "qwen") }
func TestLive_Qwen_Embed(t *testing.T)     { liveEmbed(t, "qwen") }
func TestLive_Qwen_Discovery(t *testing.T) { liveDiscovery(t, "qwen") }
