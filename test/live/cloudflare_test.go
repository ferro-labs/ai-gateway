//go:build live

package live_test

import "testing"

func TestLive_Cloudflare_Chat(t *testing.T)   { liveChat(t, "cloudflare") }
func TestLive_Cloudflare_Stream(t *testing.T) { liveStream(t, "cloudflare") }
func TestLive_Cloudflare_Embed(t *testing.T)  { liveEmbed(t, "cloudflare") }
