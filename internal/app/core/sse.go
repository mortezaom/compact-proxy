package core

import (
	"io"
	"strings"
)

type sseDecoder struct{ buffer string }

func (d *sseDecoder) feed(chunk []byte) []string {
	d.buffer += string(chunk)
	return d.frames(false)
}

func (d *sseDecoder) flush() []string { return d.frames(true) }

func (d *sseDecoder) frames(flush bool) []string {
	var result []string
	for {
		index, separator := nextSSEFrame(d.buffer)
		if index < 0 {
			break
		}
		result = append(result, d.buffer[:index])
		d.buffer = d.buffer[index+separator:]
	}
	if flush && d.buffer != "" {
		result = append(result, d.buffer)
		d.buffer = ""
	}
	return result
}

func nextSSEFrame(buffer string) (int, int) {
	lf := strings.Index(buffer, "\n\n")
	crlf := strings.Index(buffer, "\r\n\r\n")
	if lf < 0 && crlf < 0 {
		return -1, 0
	}
	if crlf >= 0 && (lf < 0 || crlf < lf) {
		return crlf, 4
	}
	return lf, 2
}

func sseData(frame string) []string {
	var result []string
	for _, line := range strings.Split(frame, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(line, "data: ") {
			result = append(result, strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
		} else if strings.HasPrefix(line, "data:") {
			result = append(result, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return result
}

func streamSSEBody(body io.ReadCloser, onFrame func(string) error) error {
	defer body.Close()
	decoder := new(sseDecoder)
	buffer := make([]byte, 32*1024)
	for {
		count, err := body.Read(buffer)
		if count > 0 {
			for _, frame := range decoder.feed(buffer[:count]) {
				if callbackErr := onFrame(frame); callbackErr != nil {
					return callbackErr
				}
			}
		}
		if err == io.EOF {
			for _, frame := range decoder.flush() {
				if callbackErr := onFrame(frame); callbackErr != nil {
					return callbackErr
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
}
