//go:build live

package live_test

import "testing"

func TestLive_XAI_Chat(t *testing.T)      { liveChat(t, "xai") }
func TestLive_XAI_Stream(t *testing.T)    { liveStream(t, "xai") }
func TestLive_XAI_Image(t *testing.T)     { liveImage(t, "xai") }
func TestLive_XAI_Discovery(t *testing.T) { liveDiscovery(t, "xai") }
