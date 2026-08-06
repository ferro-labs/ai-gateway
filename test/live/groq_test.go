//go:build live

package live_test

import "testing"

func TestLive_Groq_Chat(t *testing.T)          { liveChat(t, "groq") }
func TestLive_Groq_Stream(t *testing.T)        { liveStream(t, "groq") }
func TestLive_Groq_Transcription(t *testing.T) { liveTranscribe(t, "groq") }
func TestLive_Groq_Speech(t *testing.T)        { liveSpeak(t, "groq") }
