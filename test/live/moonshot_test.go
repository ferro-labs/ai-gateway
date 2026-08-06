//go:build live

package live_test

import "testing"

func TestLive_Moonshot_Chat(t *testing.T)      { liveChat(t, "moonshot") }
func TestLive_Moonshot_Stream(t *testing.T)    { liveStream(t, "moonshot") }
func TestLive_Moonshot_Discovery(t *testing.T) { liveDiscovery(t, "moonshot") }
