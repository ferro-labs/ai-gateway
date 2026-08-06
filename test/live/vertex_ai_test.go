//go:build live

package live_test

import "testing"

func TestLive_VertexAi_Chat(t *testing.T)   { liveChat(t, "vertex-ai") }
func TestLive_VertexAi_Stream(t *testing.T) { liveStream(t, "vertex-ai") }
func TestLive_VertexAi_Embed(t *testing.T)  { liveEmbed(t, "vertex-ai") }
func TestLive_VertexAi_Image(t *testing.T)  { liveImage(t, "vertex-ai") }
