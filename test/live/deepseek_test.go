//go:build live

package live_test

import "testing"

func TestLive_DeepSeek_Chat(t *testing.T)      { liveChat(t, "deepseek") }
func TestLive_DeepSeek_Stream(t *testing.T)    { liveStream(t, "deepseek") }
func TestLive_DeepSeek_Discovery(t *testing.T) { liveDiscovery(t, "deepseek") }
