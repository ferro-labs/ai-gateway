//go:build live

package live_test

import "testing"

func TestLive_Gemini_Chat(t *testing.T)   { liveChat(t, "gemini") }
func TestLive_Gemini_Stream(t *testing.T) { liveStream(t, "gemini") }
func TestLive_Gemini_Embed(t *testing.T)  { liveEmbed(t, "gemini") }
func TestLive_Gemini_Image(t *testing.T)  { liveImage(t, "gemini") }
