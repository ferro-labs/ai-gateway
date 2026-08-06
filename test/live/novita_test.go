//go:build live

package live_test

import "testing"

func TestLive_Novita_Chat(t *testing.T)      { liveChat(t, "novita") }
func TestLive_Novita_Stream(t *testing.T)    { liveStream(t, "novita") }
func TestLive_Novita_Embed(t *testing.T)     { liveEmbed(t, "novita") }
func TestLive_Novita_Discovery(t *testing.T) { liveDiscovery(t, "novita") }
