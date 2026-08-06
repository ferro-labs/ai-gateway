//go:build live

package live_test

import "testing"

func TestLive_Cerebras_Chat(t *testing.T)      { liveChat(t, "cerebras") }
func TestLive_Cerebras_Stream(t *testing.T)    { liveStream(t, "cerebras") }
func TestLive_Cerebras_Discovery(t *testing.T) { liveDiscovery(t, "cerebras") }
