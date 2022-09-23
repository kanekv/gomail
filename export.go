package gomail

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/mail"
	"time"

	patchedMulipart "github.com/Kane-Sendgrid/gomail/patch/mime/multipart"
	"gopkg.in/alexcesaro/quotedprintable.v2"
)

// Export converts the message into a net/mail.Message.
func (msg *Message) Export() *mail.Message {
	w := newMessageWriter(msg)

	if msg.hasMixedPart() {
		w.openMultipart("mixed")
	}

	if msg.hasRelatedPart() {
		w.openMultipart("related")
	}

	if msg.hasAlternativePart() {
		w.openMultipart("alternative")
	}
	for _, part := range msg.parts {
		h := make(map[string][]string)
		h["Mime-Version"] = []string{"1.0"}
		h["Content-Type"] = []string{part.contentType + "; charset=" + msg.charset}
		h["Content-Transfer-Encoding"] = []string{string(msg.encoding)}

		w.write(h, part.body.Bytes(), msg.encoding)
	}
	if msg.hasAlternativePart() {
		w.closeMultipart()
	}

	w.addFiles(msg.embedded, false)
	if msg.hasRelatedPart() {
		w.closeMultipart()
	}

	w.addFiles(msg.attachments, true)
	if msg.hasMixedPart() {
		w.closeMultipart()
	}
	msg.msgWriter = w

	return w.export()
}

// Reset resets all state in Message and returns all used buffers to the pool.
// The initial settings used to create the instance are preserved so the
// instance can be safely reused to create a new message.
func (msg *Message) Reset() {
	for _, part := range msg.parts {
		putBuffer(part.body)
	}
	msg.parts = nil
	if msg.msgWriter != nil {
		putBuffer(msg.msgWriter.buf)
		msg.msgWriter = nil
	}
	msg.header = make(header)
	msg.attachments = nil
	msg.embedded = nil
}

func (msg *Message) hasMixedPart() bool {
	return (len(msg.parts) > 0 && len(msg.attachments) > 0) || len(msg.attachments) > 1
}

func (msg *Message) hasRelatedPart() bool {
	return (len(msg.parts) > 0 && len(msg.embedded) > 0) || len(msg.embedded) > 1
}

func (msg *Message) hasAlternativePart() bool {
	return len(msg.parts) > 1
}

// messageWriter helps converting the message into a net/mail.Message
type messageWriter struct {
	header     map[string][]string
	buf        *bytes.Buffer
	writers    [3]*patchedMulipart.Writer
	partWriter io.Writer
	depth      uint8
}

func newMessageWriter(msg *Message) *messageWriter {
	// We copy the header so Export does not modify the message
	header := make(map[string][]string, len(msg.header)+2)
	for k, v := range msg.header {
		header[k] = v
	}

	if _, ok := header["Mime-Version"]; !ok {
		header["Mime-Version"] = []string{"1.0"}
	}
	if _, ok := header["Date"]; !ok {
		header["Date"] = []string{msg.FormatDate(now())}
	}

	return &messageWriter{header: header, buf: getBuffer()}
}

// Stubbed out for testing.
var now = time.Now

func (w *messageWriter) openMultipart(mimeType string) {
	w.writers[w.depth] = patchedMulipart.NewWriter(w.buf)
	contentType := "multipart/" + mimeType + "; boundary=" + w.writers[w.depth].Boundary()

	if w.depth == 0 {
		w.header["Content-Type"] = []string{contentType}
	} else {
		h := make(map[string][]string)
		h["Content-Type"] = []string{contentType}
		w.createPart(h)
	}
	w.depth++
}

func (w *messageWriter) createPart(h map[string][]string) {
	// No need to check the error since the underlying writer is a bytes.Buffer
	w.partWriter, _ = w.writers[w.depth-1].CreatePart(h)
}

func (w *messageWriter) closeMultipart() {
	if w.depth > 0 {
		w.writers[w.depth-1].Close()
		w.depth--
	}
}

func (w *messageWriter) addFiles(files []*File, isAttachment bool) {
	for _, f := range files {
		h := make(map[string][]string)
		h["Content-Type"] = []string{f.MimeType + "; name=\"" + f.Name + "\""}
		// as per the SetEncoding method in gomail.go, we are enforcing the encoding to be either
		// Base64, or Base64PreEncoded
		h["Content-Transfer-Encoding"] = []string{string(Base64)}
		if isAttachment {
			h["Content-Disposition"] = []string{"attachment; filename=\"" + f.Name + "\""}
		} else {
			h["Content-Disposition"] = []string{"inline; filename=\"" + f.Name + "\""}
			if f.ContentID != "" {
				h["Content-ID"] = []string{"<" + f.ContentID + ">"}
			} else {
				h["Content-ID"] = []string{"<" + f.Name + ">"}
			}
		}

		w.write(h, f.Content, f.encoding)
	}
}

func (w *messageWriter) write(h map[string][]string, body []byte, enc Encoding) {
	w.writeHeader(h)
	w.writeBody(body, enc)
}

func (w *messageWriter) writeHeader(h map[string][]string) {
	if w.depth == 0 {
		for field, value := range h {
			w.header[field] = value
		}
	} else {
		w.createPart(h)
	}
}

func (w *messageWriter) writeBody(body []byte, enc Encoding) {
	var subWriter io.Writer
	if w.depth == 0 {
		subWriter = w.buf
	} else {
		subWriter = w.partWriter
	}

	// The errors returned by writers are not checked since these writers cannot
	// return errors.
	if enc == Base64 {
		writer := base64.NewEncoder(base64.StdEncoding, newBase64LineWriter(subWriter))
		writer.Write(body)
		writer.Close()
	} else if enc == Base64PreEncoded {
		newBase64LineWriter(subWriter).Write(body)
	} else if enc == Unencoded {
		subWriter.Write(body)
	} else {
		writer := quotedprintable.NewEncoder(newQpLineWriter(subWriter))
		writer.Write(body)
	}
}

func (w *messageWriter) export() *mail.Message {
	return &mail.Message{Header: w.header, Body: w.buf}
}

// As required by RFC 2045, 6.7. (page 21) for quoted-printable, and
// RFC 2045, 6.8. (page 25) for base64.
const maxLineLen = 76

// base64LineWriter limits text encoded in base64 to 76 characters per line
type base64LineWriter struct {
	w       io.Writer
	lineLen int
}

func newBase64LineWriter(w io.Writer) *base64LineWriter {
	return &base64LineWriter{w: w}
}

func (w *base64LineWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p)+w.lineLen > maxLineLen {
		w.w.Write(p[:maxLineLen-w.lineLen])
		w.w.Write([]byte("\r\n"))
		p = p[maxLineLen-w.lineLen:]
		n += maxLineLen - w.lineLen
		w.lineLen = 0
	}

	w.w.Write(p)
	w.lineLen += len(p)

	return n + len(p), nil
}

// qpLineWriter limits text encoded in quoted-printable to 76 characters per
// line
type qpLineWriter struct {
	w       io.Writer
	lineLen int
}

func newQpLineWriter(w io.Writer) *qpLineWriter {
	return &qpLineWriter{w: w}
}

var newline []byte = []byte("=\r\n")

func (w *qpLineWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		// If the text is not over the limit, write everything
		if len(p) < maxLineLen-w.lineLen {
			w.w.Write(p)
			w.lineLen += len(p)
			return n + len(p), nil
		}

		i := bytes.IndexAny(p[:maxLineLen-w.lineLen+2], "\n")
		// If there is a newline before the limit, write the end of the line
		if i != -1 && (i != maxLineLen-w.lineLen+1 || p[i-1] == '\r') {
			w.w.Write(p[:i+1])
			p = p[i+1:]
			n += i + 1
			w.lineLen = 0
			continue
		}

		// Quoted-printable text must not be cut between an equal sign and the
		// two following characters
		var toWrite int
		if maxLineLen-w.lineLen-2 >= 0 && p[maxLineLen-w.lineLen-2] == '=' {
			toWrite = maxLineLen - w.lineLen - 2
		} else if p[maxLineLen-w.lineLen-1] == '=' {
			toWrite = maxLineLen - w.lineLen - 1
		} else {
			toWrite = maxLineLen - w.lineLen
		}

		// Insert the newline where it is needed
		w.w.Write(p[:toWrite])
		w.w.Write(newline)
		p = p[toWrite:]
		n += toWrite
		w.lineLen = 0
	}

	return n, nil
}
