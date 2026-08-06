//go:build live

package live_test

import "testing"

func TestLive_Cohere_Chat(t *testing.T)   { liveChat(t, "cohere") }
func TestLive_Cohere_Stream(t *testing.T) { liveStream(t, "cohere") }
func TestLive_Cohere_Embed(t *testing.T)  { liveEmbed(t, "cohere") }
func TestLive_Cohere_Rerank(t *testing.T) { liveRerank(t, "cohere") }
