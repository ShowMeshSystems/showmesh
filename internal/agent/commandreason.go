package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"

	"github.com/showmeshsystems/showmesh/pkg/fseq"
)

// operatorError carries the one line an operator reads for a failure. Error
// keeps the technical text for this node's log.
type operatorError struct {
	reason string
	err    error
}

func (e *operatorError) Error() string { return e.err.Error() }
func (e *operatorError) Unwrap() error { return e.err }

// operatorErrorf is an error whose own text is already operator copy.
func operatorErrorf(format string, args ...any) error {
	reason := fmt.Sprintf(format, args...)
	return &operatorError{reason: reason, err: errors.New(reason)}
}

// withOperatorReason gives err the line an operator reads in its place.
func withOperatorReason(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	return &operatorError{reason: fmt.Sprintf(format, args...), err: err}
}

const (
	reasonSequenceLacksChannels = "The sequence lacks this surface's channels. Use another sequence or fix the channels."
	reasonFileMissing           = "A file this needs is not on this node. Upload it, then try again."
	reasonDiskFull              = "This node's disk is full. Free some space, then try again."
	reasonTimedOut              = "This node ran out of time. Try again."
	reasonDownloadDamaged       = "The file arrived damaged and was discarded. This node will fetch it again."
	reasonBlackNotSaved         = "The surface is black, but that was not saved. Check this node's disk."
	reasonAudioNotSetUp         = "This node is not set up to play audio. Set its asset folder and restart it."
	reasonOperationUnknown      = "This node's ShowMesh version cannot do this. Update ShowMesh on the node."
	reasonUnconfirmed           = "The node applied this but could not confirm it. Check the node."
)

// operatorReason is the reason published in a command result for err: one
// plain line, fact then action. The technical chain stays in this node's log.
func operatorReason(action string, err error) string {
	if reason, ok := knownOperatorReason(err); ok {
		return reason
	}
	return "This node could not " + actionTask(action) + ". Check this node's log."
}

// knownOperatorReason is the specific line for err, when err has one.
func knownOperatorReason(err error) (string, bool) {
	var plain *operatorError
	if errors.As(err, &plain) {
		return plain.reason, true
	}
	var conflict stepTimeConflictError
	if errors.As(err, &conflict) {
		return conflict.Error(), true
	}
	var uncovered *fseq.ErrChannelRangeNotCovered
	switch {
	case errors.As(err, &uncovered):
		return reasonSequenceLacksChannels, true
	case errors.Is(err, fs.ErrNotExist):
		return reasonFileMissing, true
	case errors.Is(err, syscall.ENOSPC):
		return reasonDiskFull, true
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return reasonTimedOut, true
	}
	return "", false
}

// actionTask names what an operation was doing, in the operator's words.
func actionTask(action string) string {
	switch {
	case strings.HasPrefix(action, "render."), strings.HasPrefix(action, "cue.activate (render)"):
		return "change what the surface shows"
	case strings.HasPrefix(action, "audio.node.configure"), strings.HasPrefix(action, "audio.settings."):
		return "apply its audio settings"
	case strings.HasPrefix(action, "audio."):
		return "run the audio request"
	case action == "asset.fetch":
		return "download the file"
	case action == "asset.remove":
		return "remove the file"
	case strings.HasPrefix(action, "cue."):
		return "run the Cue"
	case strings.HasPrefix(action, "node.clock."):
		return "apply its clock settings"
	case strings.HasPrefix(action, "weatherdelay."):
		return "change the weather delay"
	case strings.HasPrefix(action, "fppconnect."):
		return "apply its upload settings"
	}
	return "finish the request"
}

// isOperatorSentence reports whether reason is already written as operator
// copy: it starts with a capital and ends a sentence.
func isOperatorSentence(reason string) bool {
	if reason == "" || !strings.HasSuffix(reason, ".") {
		return false
	}
	first := reason[0]
	return first >= 'A' && first <= 'Z'
}
