package edge

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

func getHeadersAndData(data []byte) (map[string]string, []byte) {
	headers := make(map[string]string)

	headerEndIndex := bytes.Index(data, []byte("\r\n\r\n"))
	if headerEndIndex == -1 {
		panic("Invalid data format")
	}

	headerLines := bytes.Split(data[:headerEndIndex], []byte("\r\n"))
	for _, line := range headerLines {
		header := bytes.SplitN(line, []byte(":"), 2)
		if len(header) == 2 {
			key := string(bytes.TrimSpace(header[0]))
			value := string(bytes.TrimSpace(header[1]))
			headers[key] = value
		}
	}

	return headers, data[headerEndIndex+4:]
}

func removeIncompatibleCharacters(str string) string {
	chars := []rune(str)

	for i, char := range chars {
		code := int(char)
		if (0 <= code && code <= 8) || (11 <= code && code <= 12) || (14 <= code && code <= 31) {
			chars[i] = ' '
		}
	}

	return string(chars)
}

func connectID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

func splitTextByByteLength(text string, byteLength int) [][]byte {
	var result [][]byte

	textBytes := []byte(text)
	if byteLength <= 0 {
		return result
	}

	for len(textBytes) > byteLength {
		splitAt := bytes.LastIndexByte(textBytes[:byteLength], ' ')
		if splitAt == -1 {
			splitAt = byteLength
		} else {
			splitAt++
		}

		for bytes.Contains(textBytes[:splitAt], []byte("&")) && !bytes.Contains(textBytes[:splitAt], []byte(";")) {
			ampersandIndex := bytes.LastIndexByte(textBytes[:splitAt], '&')
			if semicolonIndex := bytes.IndexByte(textBytes[ampersandIndex:splitAt], ';'); semicolonIndex != -1 {
				break
			}

			splitAt = ampersandIndex - 1
			if splitAt < 0 {
				panic(errors.New("Maximum byte length is too small or invalid text"))
			}
			if splitAt == 0 {
				break
			}
		}

		trimmedText := bytes.TrimSpace(textBytes[:splitAt])
		if len(trimmedText) > 0 {
			result = append(result, trimmedText)
		}
		if splitAt == 0 {
			splitAt = 1
		}
		textBytes = textBytes[splitAt:]
	}

	trimmedText := bytes.TrimSpace(textBytes)
	if len(trimmedText) > 0 {
		result = append(result, trimmedText)
	}

	return result
}

func mkssml(text string, voice string, rate string, volume string) string {
	ssml := fmt.Sprintf("<speak version='1.0' xmlns='http://www.w3.org/2001/10/synthesis' xml:lang='en-US'><voice name='%s'><prosody pitch='+0Hz' rate='%s' volume='%s'>%s</prosody></voice></speak>",
		voice, rate, volume, text)
	return ssml
}

func dateToString() string {
	// Use time.FixedZone to represent a fixed timezone offset of 0 (UTC)
	zone := time.FixedZone("UTC", 0)
	now := time.Now().In(zone)
	return now.Format("Mon Jan 02 2006 15:04:05 GMT-0700 (MST)")
}

func ssmlHeadersPlusData(requestID string, timestamp string, ssml string) string {
	headers := fmt.Sprintf("X-RequestId:%s\r\nContent-Type:application/ssml+xml\r\nX-Timestamp:%sZ\r\nPath:ssml\r\n\r\n",
		requestID, timestamp)
	return headers + ssml
}

func calcMaxMesgSize(voice string, rate string, volume string) int {
	websocketMaxSize := 1 << 16
	overheadPerMessage := len(ssmlHeadersPlusData(connectID(), dateToString(), mkssml("", voice, rate, volume))) + 50
	return websocketMaxSize - overheadPerMessage
}

func NewCommunicate(text string, opts ...Option) (*Communicate, error) {
	voiceLangRegion := ""
	voice := GetVoiceByOption(opts)
	rate := GetRateByOption(opts)
	volume := GetVolumeByOption(opts)
	proxy := GetProxyByOption(opts)
	// Default values
	if voice == "" {
		voice = "en-US, AriaNeural"
		voiceLangRegion = "en-US, AriaNeural"
	}
	if rate == "" {
		rate = "+0%"
	}
	if volume == "" {
		volume = "+0%"
	}

	// Validate voice
	validVoicePattern := regexp.MustCompile(`^([a-z]{2,})-([A-Z]{2,})-(.+Neural)$`)
	if validVoicePattern.MatchString(voice) {
		voiceLangRegion = voice
		strs := strings.Split(voice, "-")
		lang := strs[0]
		region := strs[1]
		name := strs[2]
		voice = fmt.Sprintf("Microsoft Server Speech Text to Speech Voice (%s-%s, %s)", lang, region, name)
	} else {
		return nil, errors.New("invalid voice")
	}

	// Validate rate
	validRatePattern := regexp.MustCompile(`^[+-]\d+%$`)
	if !validRatePattern.MatchString(rate) {
		return nil, errors.New("Invalid rate")
	}

	// Validate volume
	validVolumePattern := regexp.MustCompile(`^[+-]\d+%$`)
	if !validVolumePattern.MatchString(volume) {
		return nil, errors.New("Invalid volume")
	}

	return &Communicate{
		Text:            text,
		Voice:           voice,
		VoiceLangRegion: voiceLangRegion,
		Rate:            rate,
		Volume:          volume,
		Proxy:           proxy,
	}, nil
}

func escape(data string) string {
	// Must do ampersand first
	entities := make(map[string]string)
	data = html.EscapeString(data)
	data = strings.ReplaceAll(data, ">", "&gt;")
	data = strings.ReplaceAll(data, "<", "&lt;")
	if entities != nil {
		data = dictReplace(data, entities)
	}
	return data
}

func dictReplace(data string, entities map[string]string) string {
	for key, value := range entities {
		data = strings.ReplaceAll(data, key, value)
	}
	return data
}

type Communicate struct {
	Text            string
	Voice           string
	VoiceLangRegion string
	Rate            string
	Volume          string
	Proxy           string
	op              chan map[string]interface{}
}

type UnknownResponse struct {
	Message string
}

type UnexpectedResponse struct {
	Message string
}

type NoAudioReceived struct {
	Message string
}

type WebSocketError struct {
	Message string
}

func (c *Communicate) CloseOutput() {
	close(c.op)
}

func (c *Communicate) makeHeaders() http.Header {
	headers := make(http.Header)
	headers.Set("Pragma", "no-cache")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Origin", "chrome-extension://jdiccldimpdaibmpdkjnbmckianbfold")
	headers.Set("Accept-Encoding", "gzip, deflate, br")
	headers.Set("Accept-Language", "en-US,en;q=0.9")
	headers.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.77 Safari/537.36 Edg/91.0.864.41")
	return headers
}

func (c *Communicate) Stream() (<-chan map[string]interface{}, error) {
	texts := splitTextByByteLength(
		escape(removeIncompatibleCharacters(c.Text)),
		calcMaxMesgSize(c.Voice, c.Rate, c.Volume),
	)

	finalUtterance := make(map[int]int)
	prevIdx := -1
	shiftTime := -1

	output := make(chan map[string]interface{})

	for idx, text := range texts {
		fmt.Printf("text=%s\n", text)
		wsURL := WssURL + "&ConnectionId=" + connectID()
		dialer := websocket.Dialer{}
		conn, _, err := dialer.Dial(wsURL, c.makeHeaders())
		if err != nil {
			return nil, err
		}

		// download indicates whether we should be expecting audio data,
		// this is so what we avoid getting binary data from the websocket
		// and falsely thinking it's audio data.
		downloadAudio := false

		// audioWasReceived indicates whether we have received audio data
		// from the websocket. This is so we can raise an exception if we
		// don't receive any audio data.
		audioWasReceived := false

		// Each message needs to have the proper date.
		date := dateToString()

		// Prepare the request to be sent to the service.
		//
		// Note sentenceBoundaryEnabled and wordBoundaryEnabled are actually supposed
		// to be booleans, but Edge Browser seems to send them as strings.
		//
		// This is a bug in Edge as Azure Cognitive Services actually sends them as
		// bool and not string. For now I will send them as bool unless it causes
		// any problems.
		//
		// Also pay close attention to double { } in request (escape for f-string).
		err = conn.WriteMessage(websocket.TextMessage, []byte(
			"X-Timestamp:"+date+"\r\n"+
				"Content-Type:application/json; charset=utf-8\r\n"+
				"Path:speech.config\r\n\r\n"+
				`{"context":{"synthesis":{"audio":{"metadataoptions":{"sentenceBoundaryEnabled":false,"wordBoundaryEnabled":true},"outputFormat":"audio-24khz-48kbitrate-mono-mp3"}}}}`+"\r\n",
		))
		if err != nil {
			conn.Close()
			return nil, err
		}

		err = conn.WriteMessage(websocket.TextMessage, []byte(
			ssmlHeadersPlusData(
				connectID(),
				date,
				mkssml(string(text), c.Voice, c.Rate, c.Volume),
			),
		))
		if err != nil {
			conn.Close()
			return nil, err
		}
		idx := idx

		go func() {
			defer conn.Close()

			for {
				msgType, message, err := conn.ReadMessage()
				if err != nil {
					if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
						// WebSocket error
						output <- map[string]interface{}{
							"error": WebSocketError{Message: err.Error()},
						}
					}
					break
				}

				if msgType == websocket.TextMessage {
					parameters, data := getHeadersAndData(message)
					path := parameters["Path"]
					if path == "turn.start" {
						downloadAudio = true
					} else if path == "turn.end" {
						downloadAudio = false
						break // End of audio data
					} else if path == "audio.metadata" {
						var metadata struct {
							Metadata []struct {
								Type string `json:"Type"`
								Data struct {
									Offset   int `json:"Offset"`
									Duration int `json:"Duration"`
									Text     struct {
										Text         string `json:"Text"`
										Length       int64  `json:"Length"`
										BoundaryType string `json:"BoundaryType"`
									} `json:"text"`
								} `json:"Data"`
							} `json:"Metadata"`
						}
						err := json.Unmarshal([]byte(data), &metadata)
						if err != nil {
							msg := fmt.Sprintf("err=%s, data=%s", err.Error(), string(data))
							output <- map[string]interface{}{
								"error": UnknownResponse{Message: msg},
							}
							break
						}

						for _, metaObj := range metadata.Metadata {
							metaType := metaObj.Type
							if idx != prevIdx {
								shiftTime = sum(idx, finalUtterance)
								prevIdx = idx
							}
							if metaType == "WordBoundary" {
								finalUtterance[idx] = metaObj.Data.Offset + metaObj.Data.Duration + 8_750_000
								output <- map[string]interface{}{
									"type":     metaType,
									"offset":   metaObj.Data.Offset + shiftTime,
									"duration": metaObj.Data.Duration,
									"text":     metaObj.Data.Text,
								}
							} else if metaType == "SessionEnd" {
								continue
							} else {
								output <- map[string]interface{}{
									"error": UnknownResponse{Message: "Unknown metadata type: " + metaType},
								}
								break
							}
						}
					} else if path == "response" {
						// Do nothing
					} else {
						output <- map[string]interface{}{
							"error": UnknownResponse{Message: "The response from the service is not recognized.\n" + string(message)},
						}
						break
					}
				} else if msgType == websocket.BinaryMessage {
					if !downloadAudio {
						output <- map[string]interface{}{
							"error": UnknownResponse{"We received a binary message, but we are not expecting one."},
						}
					}

					if len(message) < 2 {
						output <- map[string]interface{}{
							"error": UnknownResponse{"We received a binary message, but it is missing the header length."},
						}
					}

					headerLength := int(binary.BigEndian.Uint16(message[:2]))
					if len(message) < headerLength+2 {
						output <- map[string]interface{}{
							"error": UnknownResponse{"We received a binary message, but it is missing the audio data."},
						}
					}

					audioData := message[headerLength+2:]
					output <- map[string]interface{}{
						"type": "audio",
						"data": audioData,
					}
					audioWasReceived = true
				} else {
					if message != nil {
						output <- map[string]interface{}{
							"error": WebSocketError{
								Message: string(message),
							},
						}
					} else {
						output <- map[string]interface{}{
							"error": WebSocketError{
								Message: "Unknown error",
							},
						}
					}
				}

			}

			if !audioWasReceived {
				output <- map[string]interface{}{
					"error": NoAudioReceived{Message: "No audio was received. Please verify that your parameters are correct."},
				}
			}
			close(output)
		}()
	}
	c.op = output
	return output, nil
}

func sum(idx int, m map[int]int) int {
	sum := 0
	for i := 0; i < idx; i++ {
		sum += m[i]
	}
	return sum

}
