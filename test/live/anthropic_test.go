//go:build live

package live_test

import "testing"

func TestLive_Anthropic_Chat(t *testing.T)      { liveChat(t, "anthropic") }
func TestLive_Anthropic_Stream(t *testing.T)    { liveStream(t, "anthropic") }
func TestLive_Anthropic_Discovery(t *testing.T) { liveDiscovery(t, "anthropic") }
