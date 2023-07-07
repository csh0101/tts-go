package tts

import "github.com/csh0101/tts-go/edge"

func GenTTS() (string, error) {
	c, err := edge.NewCommunicate("cpdd,cpdd,cpdd")
	if err != nil {
		return "", err
	}

	speech := &Speech{
		Communicate: c,
		folder:      "audio",
	}
	err = speech.getOrGen()
	if err != nil {
		return "", err
	}
	return speech.fileName, nil
}
