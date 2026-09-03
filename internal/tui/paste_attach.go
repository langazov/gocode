package tui

import (
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// This file ports pastedFilepath, readLocalAttachment and pasteAttachment from
// packages/tui/src/component/prompt/. Pasting a path to an image (which is
// what a file manager or a screenshot tool puts on the clipboard) attaches the
// file rather than inserting its path as text.

// attachmentMimes ports the mimeTypes table in local-attachment.ts. The
// extension decides the type: the file is not sniffed, matching the original.
var attachmentMimes = map[string]string{
	".avif": "image/avif",
	".gif":  "image/gif",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".pdf":  "application/pdf",
	".png":  "image/png",
	".svg":  "image/svg+xml",
	".webp": "image/webp",
}

// pastedFilepath normalizes a pasted path, porting pastedFilepath: strip
// surrounding quotes, resolve a file:// URL, and undo the backslash escaping a
// shell-style drag-and-drop adds on POSIX.
func pastedFilepath(value string) string {
	raw := strings.Trim(strings.TrimSpace(value), `'"`)
	if strings.HasPrefix(raw, "file://") {
		if parsed, err := url.Parse(raw); err == nil {
			path := parsed.Path
			if runtime.GOOS == "windows" && len(path) > 2 && path[0] == '/' && path[2] == ':' {
				path = path[1:]
			}
			if decoded, err := url.PathUnescape(path); err == nil {
				return decoded
			}
			return path
		}
	}
	if runtime.GOOS == "windows" {
		return raw
	}
	return unescapeBackslashes(raw)
}

// unescapeBackslashes ports `raw.replace(/\\(.)/g, "$1")`.
func unescapeBackslashes(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+1 < len(value) {
			i++
			out.WriteByte(value[i])
			continue
		}
		out.WriteByte(value[i])
	}
	return out.String()
}

// localAttachment is a file resolved from a pasted path.
type localAttachment struct {
	// Text is set for SVG, which the original inlines as text rather than
	// attaching as an image.
	Text string
	// Data is the base64 payload for a binary attachment.
	Data string
	Mime string
	Name string
}

// readLocalAttachment ports readLocalAttachmentWith: an extension this port
// knows, and a file that exists, becomes an attachment. Anything else returns
// false so the paste falls through to being inserted as text.
func readLocalAttachment(path string) (localAttachment, bool) {
	mime, known := attachmentMimes[strings.ToLower(filepath.Ext(path))]
	if !known {
		return localAttachment{}, false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return localAttachment{}, false
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return localAttachment{}, false
	}
	name := filepath.Base(path)
	if mime == "image/svg+xml" {
		// SVG is markup, and the original pastes it as text so the model can
		// read it rather than needing vision.
		return localAttachment{Text: string(contents), Mime: mime, Name: name}, true
	}
	return localAttachment{
		Data: base64.StdEncoding.EncodeToString(contents),
		Mime: mime,
		Name: name,
	}, true
}

// attachPaste inserts a placeholder for an attachment and records it for
// submit, porting pasteAttachment.
func (a *App) attachPaste(file localAttachment) {
	label := "Image"
	if file.Mime == "application/pdf" {
		label = "PDF"
	}
	// Numbered per kind, as the original does, so two images read as
	// "[Image 1]" and "[Image 2]".
	count := 1
	for _, existing := range a.attachments {
		if attachmentLabel(existing.file.Mime) == label {
			count++
		}
	}
	placeholder := "[" + label + " " + itoa(count) + "]"

	a.attachments = append(a.attachments, pastedAttachment{
		placeholder: placeholder,
		file: client.FileAttachment{
			URI:  "data:" + file.Mime + ";base64," + file.Data,
			Mime: file.Mime,
			Name: file.Name,
		},
	})
	a.input.InsertString(placeholder + " ")
}

func attachmentLabel(mime string) string {
	if mime == "application/pdf" {
		return "PDF"
	}
	return "Image"
}

// pastedAttachment is a file standing in the prompt as a placeholder.
type pastedAttachment struct {
	placeholder string
	file        client.FileAttachment
}

// takeAttachments returns the pending attachments and clears them.
func (a *App) takeAttachments() []client.FileAttachment {
	if len(a.attachments) == 0 {
		return nil
	}
	files := make([]client.FileAttachment, 0, len(a.attachments))
	for _, item := range a.attachments {
		files = append(files, item.file)
	}
	a.attachments = nil
	return files
}

// itoa avoids pulling strconv in for one call.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
