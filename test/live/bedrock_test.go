//go:build live

package live_test

import "testing"

func TestLive_Bedrock_Chat(t *testing.T)   { liveChat(t, "bedrock") }
func TestLive_Bedrock_Stream(t *testing.T) { liveStream(t, "bedrock") }
func TestLive_Bedrock_Embed(t *testing.T)  { liveEmbed(t, "bedrock") }
func TestLive_Bedrock_Image(t *testing.T)  { liveImage(t, "bedrock") }
func TestLive_Bedrock_Rerank(t *testing.T) { liveRerank(t, "bedrock") }
