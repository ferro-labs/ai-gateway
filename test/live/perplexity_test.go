//go:build live

package live_test

import "testing"

func TestLive_Perplexity_Chat(t *testing.T)   { liveChat(t, "perplexity") }
func TestLive_Perplexity_Stream(t *testing.T) { liveStream(t, "perplexity") }
