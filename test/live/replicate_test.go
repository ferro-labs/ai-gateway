//go:build live

package live_test

import "testing"

func TestLive_Replicate_Chat(t *testing.T)   { liveChat(t, "replicate") }
func TestLive_Replicate_Stream(t *testing.T) { liveStream(t, "replicate") }
func TestLive_Replicate_Image(t *testing.T)  { liveImage(t, "replicate") }
