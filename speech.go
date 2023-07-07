package tts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/csh0101/tts-go/edge"
)

type Speech struct {
	*edge.Communicate
	file     *os.File
	folder   string
	fileName string
}

func (s *Speech) getOrGen() error {
	fileName := s.folder + "/" + generateHashName(s.Text, s.VoiceLangRegion) + ".mp3"
	s.fileName = fileName
	if s.isSpeechExist(fileName) {
		return nil
	}
	err := s.createFile(fileName)
	if err != nil {
		return err
	}
	defer s.file.Close()

	err = s.gen()
	if err != nil {
		return err
	}
	return s.gen()
	return nil
}

func (s *Speech) gen() error {
	op, err := s.Stream()
	if err != nil {
		return err
	}
	for i := range op {
		t, ok := i["type"]
		if ok && t == "audio" {
			data := i["data"].([]byte)
			s.file.Write(data)
		}
		e, ok := i["error"]
		if ok {
			fmt.Printf("has error err: %v\n", e)
		}
	}
	return nil
}

func (s *Speech) isSpeechExist(fileName string) bool {
	file, err := os.Open(fileName)
	if os.IsNotExist(err) {
		return false
	}
	file.Close()
	return true
}

func (s *Speech) createFile(fileName string) error {
	// if file exist than return
	// else create it
	file, err := os.Open(fileName)
	if err == nil {
		return nil
	} else {
		file, err = os.Create(fileName)
		if err != nil {
			return err
		}
	}
	s.file = file
	return nil
}

func generateHashName(name, voice string) string {
	hash := sha256.Sum256([]byte(name))
	return fmt.Sprintf("%s_%s", voice, hex.EncodeToString(hash[:]))
}
